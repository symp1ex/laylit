//go:build windows

package entry

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"laylit/internal/sessionipc"
)

func TestHelperCommandLinePreservesPathsWithSpaces(t *testing.T) {
	want := []string{`C:\Program Files\Laylit\Laylit.exe`, sessionHelperArgument, `\\.\pipe\Laylit session`, `nonce with spaces`}
	got, err := windows.DecomposeCommandLine(composeHelperCommandLine(want[0], want[2], want[3]))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("helper command line decoded as %q, want %q", got, want)
	}
}

func TestNoInteractiveUserTokenIsAnUsableAbsence(t *testing.T) {
	for _, err := range []error{
		windows.ERROR_NO_TOKEN,
		windows.ERROR_NOT_LOGGED_ON,
		windows.ERROR_NO_SUCH_LOGON_SESSION,
		windows.ERROR_CTX_WINSTATION_NOT_FOUND,
	} {
		if !isNoInteractiveUserToken(err) {
			t.Errorf("isNoInteractiveUserToken(%v) = false", err)
		}
	}
	if isNoInteractiveUserToken(errors.New("access denied")) {
		t.Fatal("unrelated error was treated as absence of an interactive user")
	}
}

func TestLifecycleNamedPipeHandshake(t *testing.T) {
	nonce, err := randomProtocolToken()
	if err != nil {
		t.Fatal(err)
	}
	pipeName := `\\.\pipe\Laylit-test-` + nonce
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	tokenUser, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	pipe, err := createLifecyclePipe(pipeName, tokenUser.User.Sid)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sessionID, err := currentProcessSessionID()
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() {
		connection, acceptErr := acceptLifecyclePipe(ctx, pipe, pipeName)
		if acceptErr != nil {
			_ = windows.CloseHandle(pipe)
			serverDone <- acceptErr
			return
		}
		defer connection.Close()
		if _, err := readHelperHello(ctx, connection, nonce, sessionID); err != nil {
			serverDone <- err
			return
		}
		ready, err := sessionipc.ReadFrame(connection)
		if err == nil && ready.Type != sessionipc.TypeReady {
			err = errors.New("unexpected helper readiness frame")
		}
		serverDone <- err
	}()

	client, err := connectSessionPipe(ctx, pipeName)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	hello, err := sessionipc.NewFrame(sessionipc.TypeHello, 0, 0, sessionipc.HelloMeta{
		Nonce: nonce, PID: os.Getpid(), SessionID: sessionID, Role: sessionHelperRole,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sessionipc.WriteFrame(client, hello); err != nil {
		t.Fatal(err)
	}
	ready, err := sessionipc.NewFrame(sessionipc.TypeReady, 0, 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sessionipc.WriteFrame(client, ready); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("named-pipe lifecycle handshake timed out")
	}
}
