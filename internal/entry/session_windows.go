//go:build windows

package entry

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	invalidConsoleSessionID = ^uint32(0)
	helperStartupTimeout    = 20 * time.Second
	helperConnectRetry      = 100 * time.Millisecond
	helperForcedExitWait    = 5 * time.Second
)

func activePhysicalConsoleSession() (uint32, bool, error) {
	sessionID := windows.WTSGetActiveConsoleSessionId()
	if sessionID == invalidConsoleSessionID {
		return 0, false, nil
	}
	var token windows.Token
	if err := windows.WTSQueryUserToken(sessionID, &token); err != nil {
		if isNoInteractiveUserToken(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	_ = token.Close()
	return sessionID, true, nil
}

func isNoInteractiveUserToken(err error) bool {
	return errors.Is(err, windows.ERROR_NO_TOKEN) ||
		errors.Is(err, windows.ERROR_NOT_LOGGED_ON) ||
		errors.Is(err, windows.ERROR_NO_SUCH_LOGON_SESSION) ||
		errors.Is(err, windows.ERROR_CTX_WINSTATION_NOT_FOUND)
}

func startSessionHelper(ctx context.Context, sessionID uint32) (helperProcess, error) {
	startupCtx, cancel := context.WithTimeout(ctx, helperStartupTimeout)
	defer cancel()
	if current := windows.WTSGetActiveConsoleSessionId(); current != sessionID {
		return nil, fmt.Errorf("physical console session changed from %d to %d before helper launch", sessionID, current)
	}

	nonce, err := randomProtocolToken()
	if err != nil {
		return nil, err
	}
	var token windows.Token
	if err := windows.WTSQueryUserToken(sessionID, &token); err != nil {
		return nil, fmt.Errorf("query user token for console session %d: %w", sessionID, err)
	}
	defer token.Close()
	tokenUser, err := token.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("query user SID for console session %d: %w", sessionID, err)
	}
	pipeName := `\\.\pipe\Laylit-session-` + nonce
	pipe, err := createLifecyclePipe(pipeName, tokenUser.User.Sid)
	if err != nil {
		return nil, err
	}
	pipeOwned := true
	defer func() {
		if pipeOwned {
			_ = windows.CloseHandle(pipe)
		}
	}()

	process, err := createProcessInSession(token, sessionID, pipeName, nonce)
	if err != nil {
		return nil, err
	}
	processOwned := true
	defer func() {
		if processOwned {
			terminateAndCloseProcess(process)
		}
	}()

	connection, err := acceptLifecyclePipe(startupCtx, pipe, pipeName)
	if err != nil {
		return nil, fmt.Errorf("accept helper lifecycle connection: %w", err)
	}
	pipeOwned = false
	connectionOwned := true
	defer func() {
		if connectionOwned {
			_ = connection.Close()
		}
	}()
	helper := newWindowsHelperProcess(sessionID, process, connection)
	processOwned = false
	connectionOwned = false
	if err := awaitHelperReady(startupCtx, connection, nonce); err != nil {
		_ = connection.SetDeadline(time.Time{})
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		stopErr := helper.Stop(stopCtx)
		stopCancel()
		return nil, errors.Join(err, stopErr)
	}
	return helper, nil
}

func randomProtocolToken() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate lifecycle token: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

func createLifecyclePipe(name string, userSID *windows.SID) (windows.Handle, error) {
	namePointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;%s)", userSID.String()))
	if err != nil {
		return 0, fmt.Errorf("create lifecycle pipe security descriptor: %w", err)
	}
	attributes := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	handle, err := windows.CreateNamedPipe(
		namePointer,
		windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		1, maxProtocolLine, maxProtocolLine, uint32(helperStartupTimeout/time.Millisecond), &attributes,
	)
	runtime.KeepAlive(descriptor)
	if err != nil {
		return 0, fmt.Errorf("create lifecycle pipe: %w", err)
	}
	return handle, nil
}

func acceptLifecyclePipe(ctx context.Context, pipe windows.Handle, name string) (*os.File, error) {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("create pipe connection event: %w", err)
	}
	defer windows.CloseHandle(event)
	overlapped := windows.Overlapped{HEvent: event}
	err = windows.ConnectNamedPipe(pipe, &overlapped)
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		connected := make(chan error, 1)
		go func() {
			waitResult, waitErr := windows.WaitForSingleObject(event, windows.INFINITE)
			if waitErr == nil && waitResult != windows.WAIT_OBJECT_0 {
				waitErr = fmt.Errorf("unexpected pipe connection wait result %d", waitResult)
			}
			if waitErr == nil {
				var transferred uint32
				waitErr = windows.GetOverlappedResult(pipe, &overlapped, &transferred, false)
			}
			connected <- waitErr
		}()
		select {
		case err = <-connected:
		case <-ctx.Done():
			_ = windows.CancelIoEx(pipe, &overlapped)
			<-connected
			return nil, ctx.Err()
		}
	}
	if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		return nil, err
	}
	file := os.NewFile(uintptr(pipe), name)
	if file == nil {
		return nil, errors.New("wrap lifecycle pipe handle")
	}
	return file, nil
}

func awaitHelperReady(ctx context.Context, connection *os.File, nonce string) error {
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return fmt.Errorf("set lifecycle handshake deadline: %w", err)
		}
	}
	watchDone := make(chan struct{})
	watchExited := make(chan struct{})
	go func() {
		defer close(watchExited)
		select {
		case <-ctx.Done():
			_ = connection.SetDeadline(time.Now())
		case <-watchDone:
		}
	}()
	watching := true
	defer func() {
		if watching {
			close(watchDone)
			<-watchExited
		}
	}()

	reader := bufio.NewReaderSize(connection, maxProtocolLine)
	hello, err := readProtocolLine(reader)
	if err != nil {
		return fmt.Errorf("read helper hello: %w", err)
	}
	if hello != helperHelloPrefix+nonce {
		return errors.New("helper lifecycle authentication failed")
	}
	ready, err := readProtocolLine(reader)
	if err != nil {
		return fmt.Errorf("read helper readiness: %w", err)
	}
	if ready != helperReady {
		return fmt.Errorf("unexpected helper lifecycle response %q", ready)
	}
	close(watchDone)
	<-watchExited
	watching = false
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear lifecycle handshake deadline: %w", err)
	}
	return nil
}

func createProcessInSession(token windows.Token, sessionID uint32, pipeName, nonce string) (windows.Handle, error) {
	var environment *uint16
	if err := windows.CreateEnvironmentBlock(&environment, token, false); err != nil {
		return 0, fmt.Errorf("create user environment for console session %d: %w", sessionID, err)
	}
	defer windows.DestroyEnvironmentBlock(environment)

	executable, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("determine helper executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return 0, fmt.Errorf("make helper executable path absolute: %w", err)
	}
	applicationName, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return 0, err
	}
	commandLine, err := windows.UTF16FromString(composeHelperCommandLine(executable, pipeName, nonce))
	if err != nil {
		return 0, err
	}
	desktop, err := windows.UTF16PtrFromString(`winsta0\default`)
	if err != nil {
		return 0, err
	}
	workingDirectory, err := windows.UTF16PtrFromString(filepath.Dir(executable))
	if err != nil {
		return 0, err
	}
	startup := windows.StartupInfo{
		Cb:         uint32(unsafe.Sizeof(windows.StartupInfo{})),
		Desktop:    desktop,
		Flags:      windows.STARTF_USESHOWWINDOW,
		ShowWindow: windows.SW_HIDE,
	}
	var process windows.ProcessInformation
	if err := windows.CreateProcessAsUser(
		token, applicationName, &commandLine[0], nil, nil, false,
		windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NO_WINDOW,
		environment, workingDirectory, &startup, &process,
	); err != nil {
		return 0, fmt.Errorf("create helper in console session %d: %w", sessionID, err)
	}
	_ = windows.CloseHandle(process.Thread)
	return process.Process, nil
}

func composeHelperCommandLine(executable, pipeName, nonce string) string {
	return windows.ComposeCommandLine([]string{executable, sessionHelperArgument, pipeName, nonce})
}

func connectSessionPipe(ctx context.Context, name string) (*os.File, error) {
	connectCtx, cancel := context.WithTimeout(ctx, helperStartupTimeout)
	defer cancel()
	namePointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	for {
		handle, openErr := windows.CreateFile(
			namePointer, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
			windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0,
		)
		if openErr == nil {
			file := os.NewFile(uintptr(handle), name)
			if file == nil {
				_ = windows.CloseHandle(handle)
				return nil, errors.New("wrap lifecycle pipe connection")
			}
			return file, nil
		}
		if !errors.Is(openErr, windows.ERROR_PIPE_BUSY) && !errors.Is(openErr, windows.ERROR_FILE_NOT_FOUND) {
			return nil, openErr
		}
		timer := time.NewTimer(helperConnectRetry)
		select {
		case <-connectCtx.Done():
			timer.Stop()
			return nil, connectCtx.Err()
		case <-timer.C:
		}
	}
}

type windowsHelperProcess struct {
	sessionID  uint32
	connection *os.File
	done       chan struct{}

	mu        sync.Mutex
	process   windows.Handle
	exitError error
}

func newWindowsHelperProcess(sessionID uint32, process windows.Handle, connection *os.File) *windowsHelperProcess {
	helper := &windowsHelperProcess{sessionID: sessionID, process: process, connection: connection, done: make(chan struct{})}
	go helper.wait()
	return helper
}

func (helper *windowsHelperProcess) SessionID() uint32     { return helper.sessionID }
func (helper *windowsHelperProcess) Done() <-chan struct{} { return helper.done }

func (helper *windowsHelperProcess) ExitError() error {
	helper.mu.Lock()
	defer helper.mu.Unlock()
	return helper.exitError
}

func (helper *windowsHelperProcess) wait() {
	helper.mu.Lock()
	process := helper.process
	helper.mu.Unlock()
	waitResult, waitErr := windows.WaitForSingleObject(process, windows.INFINITE)
	if waitErr == nil && waitResult != windows.WAIT_OBJECT_0 {
		waitErr = fmt.Errorf("unexpected process wait result %d", waitResult)
	}
	if waitErr == nil {
		var exitCode uint32
		if err := windows.GetExitCodeProcess(process, &exitCode); err != nil {
			waitErr = fmt.Errorf("read helper exit code: %w", err)
		} else if exitCode != 0 {
			waitErr = fmt.Errorf("helper exited with code %d", exitCode)
		}
	}
	_ = helper.connection.Close()
	helper.mu.Lock()
	helper.exitError = waitErr
	_ = windows.CloseHandle(helper.process)
	helper.process = 0
	helper.mu.Unlock()
	close(helper.done)
}

func (helper *windowsHelperProcess) Stop(ctx context.Context) error {
	if deadline, ok := ctx.Deadline(); ok {
		_ = helper.connection.SetWriteDeadline(deadline)
	}
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- writeProtocolLine(helper.connection, helperStop)
	}()
	var writeErr error
	select {
	case writeErr = <-writeDone:
	case <-ctx.Done():
		_ = helper.connection.Close()
		writeErr = errors.Join(ctx.Err(), <-writeDone)
	}

	select {
	case <-helper.done:
		return errors.Join(writeErr, helper.ExitError())
	case <-ctx.Done():
	}
	_ = helper.connection.Close()
	killErr := helper.terminate()
	select {
	case <-helper.done:
		return errors.Join(writeErr, errors.New("helper did not stop gracefully and was terminated"), killErr)
	case <-time.After(helperForcedExitWait):
		return errors.Join(writeErr, errors.New("helper did not exit after forced termination"), killErr)
	}
}

func (helper *windowsHelperProcess) terminate() error {
	helper.mu.Lock()
	defer helper.mu.Unlock()
	if helper.process == 0 {
		return nil
	}
	return windows.TerminateProcess(helper.process, 1)
}

func terminateAndCloseProcess(process windows.Handle) {
	_ = windows.TerminateProcess(process, 1)
	_, _ = windows.WaitForSingleObject(process, uint32(helperForcedExitWait/time.Millisecond))
	_ = windows.CloseHandle(process)
}
