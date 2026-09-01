// layout-probe records the real Win32 messages delivered to the same kind of
// hidden window used by the production layout source. It is diagnostic only.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmClose                  = 0x0010
	wmDestroy                = 0x0002
	wmInputLangChangeRequest = 0x0050
	wmInputLangChange        = 0x0051
	wmKeyboardChordCompleted = 0x8001
	hshelLHighBit            = 0x8000
)

var (
	user32                        = windows.NewLazySystemDLL("user32.dll")
	kernel32                      = windows.NewLazySystemDLL("kernel32.dll")
	procRegisterClassExW          = user32.NewProc("RegisterClassExW")
	procUnregisterClassW          = user32.NewProc("UnregisterClassW")
	procCreateWindowExW           = user32.NewProc("CreateWindowExW")
	procDestroyWindow             = user32.NewProc("DestroyWindow")
	procDefWindowProcW            = user32.NewProc("DefWindowProcW")
	procGetMessageW               = user32.NewProc("GetMessageW")
	procTranslateMessage          = user32.NewProc("TranslateMessage")
	procDispatchMessageW          = user32.NewProc("DispatchMessageW")
	procPostMessageW              = user32.NewProc("PostMessageW")
	procPostQuitMessage           = user32.NewProc("PostQuitMessage")
	procRegisterWindowMessageW    = user32.NewProc("RegisterWindowMessageW")
	procRegisterShellHookWindow   = user32.NewProc("RegisterShellHookWindow")
	procDeregisterShellHookWindow = user32.NewProc("DeregisterShellHookWindow")
	procGetModuleHandleW          = kernel32.NewProc("GetModuleHandleW")
	windowProcedure               = syscall.NewCallback(windowProc)
	probeWindows                  sync.Map
)

func main() {
	os.Exit(run())
}

func run() int {
	outputPath := flag.String("output", "layout-probe.log", "path for the diagnostic log")
	flag.Parse()

	file, err := os.Create(*outputPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create probe log:", err)
		return 1
	}
	defer file.Close()

	logger := log.New(io.MultiWriter(os.Stdout, file), "", 0)
	if foregroundSnapshot().hwnd == 0 {
		logger.Printf("timestamp=%s phase=startup invalid=true reason=no-foreground-window", timestamp())
		logger.Print("This process is not attached to an interactive input desktop; run it from a normal user console.")
		return 2
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	state, err := startProbe(logger)
	if err != nil {
		logger.Printf("timestamp=%s phase=startup error=%q", timestamp(), err)
		return 1
	}
	defer state.close()

	state.logSnapshot("startup-ready", "registered=true")
	logger.Print("Probe ready. Use Alt+Shift, Win+Space, and the Windows language UI in another app; switch foreground windows too. Return here and press Enter to stop.")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	defer signal.Stop(stop)
	go func() {
		select {
		case <-stop:
		case <-readEnter():
		}
		procPostMessageW.Call(state.hwnd, wmClose, 0, 0)
	}()

	var message msg
	for {
		result, _, callErr := procGetMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(result) == -1 {
			logger.Printf("timestamp=%s phase=message-loop error=%q", timestamp(), win32Error("GetMessage", callErr))
			return 1
		}
		if result == 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
	}
	logger.Printf("timestamp=%s phase=shutdown result=clean", timestamp())
	return 0
}

type probeState struct {
	logger        *log.Logger
	hwnd          uintptr
	shellMessage  uint32
	listenerTID   uint32
	lastID        string
	tsf           *tsfProbe
	keyboard      *keyboardProbe
	deregistered  bool
	className     *uint16
	module        uintptr
	windowCreated bool
}

func startProbe(logger *log.Logger) (*probeState, error) {
	className, err := windows.UTF16PtrFromString(fmt.Sprintf("Laylit.LayoutProbe.%d", os.Getpid()))
	if err != nil {
		return nil, err
	}
	module, _, callErr := procGetModuleHandleW.Call(0)
	if module == 0 {
		return nil, win32Error("GetModuleHandle", callErr)
	}
	class := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   windowProcedure,
		HInstance:     module,
		LpszClassName: className,
	}
	atom, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		return nil, win32Error("RegisterClassEx", callErr)
	}
	state := &probeState{logger: logger, className: className, module: module, listenerTID: windows.GetCurrentThreadId()}

	hwnd, _, callErr := procCreateWindowExW.Call(
		0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(className)),
		0, 0, 0, 0, 0, 0, 0, module, 0,
	)
	if hwnd == 0 {
		state.close()
		return nil, win32Error("CreateWindowEx", callErr)
	}
	state.hwnd = hwnd
	state.windowCreated = true

	messageName, _ := windows.UTF16PtrFromString("SHELLHOOK")
	shellMessage, _, callErr := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(messageName)))
	if shellMessage == 0 {
		state.close()
		return nil, win32Error("RegisterWindowMessage(SHELLHOOK)", callErr)
	}
	state.shellMessage = uint32(shellMessage)
	probeWindows.Store(hwnd, state)

	registered, _, callErr := procRegisterShellHookWindow.Call(hwnd)
	if registered == 0 {
		state.close()
		return nil, win32Error("RegisterShellHookWindow", callErr)
	}
	tsf, err := startTSFProbe(logger)
	if err != nil {
		state.close()
		return nil, err
	}
	state.tsf = tsf
	keyboard, err := startKeyboardProbe(state)
	if err != nil {
		state.close()
		return nil, err
	}
	state.keyboard = keyboard
	return state, nil
}

func (state *probeState) close() {
	if state.keyboard != nil {
		state.keyboard.close()
		state.keyboard = nil
	}
	if state.tsf != nil {
		state.tsf.close()
		state.tsf = nil
	}
	if state.hwnd != 0 {
		probeWindows.Delete(state.hwnd)
	}
	if state.hwnd != 0 && !state.deregistered {
		procDeregisterShellHookWindow.Call(state.hwnd)
		state.deregistered = true
	}
	if state.hwnd != 0 && state.windowCreated {
		procDestroyWindow.Call(state.hwnd)
		state.windowCreated = false
	}
	if state.className != nil && state.module != 0 {
		procUnregisterClassW.Call(uintptr(unsafe.Pointer(state.className)), state.module)
		state.className = nil
	}
}

func windowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	value, found := probeWindows.Load(hwnd)
	state, stateOK := value.(*probeState)
	if found && stateOK {
		switch message {
		case state.shellMessage:
			baseCode := wParam &^ hshelLHighBit
			highBit := wParam&hshelLHighBit != 0
			state.logWindowMessage("shellhook", message, wParam, baseCode, highBit, lParam)
			state.publishCurrent(fmt.Sprintf("shellhook-base-%d", baseCode))
		case wmInputLangChangeRequest:
			state.logWindowMessage("wm-inputlangchangerequest", message, wParam, 0, false, lParam)
		case wmInputLangChange:
			state.logWindowMessage("wm-inputlangchange", message, wParam, 0, false, lParam)
			state.publishCurrent("wm-inputlangchange")
			return 1
		case wmKeyboardChordCompleted:
			state.logSnapshot("keyboard-chord-completed", fmt.Sprintf("vk=0x%X keyboard_message=0x%X", wParam, lParam))
			state.publishCurrent("keyboard-alt-shift-completed")
		}
	}

	switch message {
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	result, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
	return result
}

func (state *probeState) logWindowMessage(phase string, message uint32, wParam, baseCode uintptr, highBit bool, lParam uintptr) {
	snapshot := foregroundSnapshot()
	state.logger.Printf(
		"timestamp=%s phase=%s wndproc=true message=0x%04X shell_message=0x%04X wparam=0x%X base_code=0x%X high_bit=%t lparam=0x%X foreground_hwnd=0x%X foreground_pid=%d foreground_tid=%d foreground_hkl=0x%X listener_tid=%d listener_hkl=0x%X",
		timestamp(), phase, message, state.shellMessage, wParam, baseCode, highBit, lParam,
		snapshot.hwnd, snapshot.pid, snapshot.threadID, snapshot.hkl, state.listenerTID, uintptr(windows.GetKeyboardLayout(state.listenerTID)),
	)
}

func (state *probeState) publishCurrent(reason string) {
	snapshot := foregroundSnapshot()
	if snapshot.hwnd == 0 || snapshot.threadID == 0 || snapshot.hkl == 0 {
		state.logger.Printf("timestamp=%s phase=publish reason=%s result=error foreground_hwnd=0x%X foreground_tid=%d foreground_hkl=0x%X", timestamp(), reason, snapshot.hwnd, snapshot.threadID, snapshot.hkl)
		return
	}
	id := stableLayoutID(snapshot.hkl)
	if id == state.lastID {
		state.logger.Printf("timestamp=%s phase=publish reason=%s layout_id=%s result=deduplicated", timestamp(), reason, id)
		return
	}
	state.lastID = id
	state.logger.Printf("timestamp=%s phase=publish reason=%s layout_id=%s result=published", timestamp(), reason, id)
}

func (state *probeState) logSnapshot(phase, fields string) {
	snapshot := foregroundSnapshot()
	state.logger.Printf(
		"timestamp=%s phase=%s %s shell_message=0x%04X foreground_hwnd=0x%X foreground_pid=%d foreground_tid=%d foreground_hkl=0x%X listener_tid=%d listener_hkl=0x%X",
		timestamp(), phase, fields, state.shellMessage, snapshot.hwnd, snapshot.pid, snapshot.threadID, snapshot.hkl,
		state.listenerTID, uintptr(windows.GetKeyboardLayout(state.listenerTID)),
	)
}

type snapshot struct {
	hwnd     uintptr
	pid      uint32
	threadID uint32
	hkl      uintptr
}

func foregroundSnapshot() snapshot {
	hwnd := windows.GetForegroundWindow()
	if hwnd == 0 {
		return snapshot{}
	}
	var processID uint32
	threadID, _ := windows.GetWindowThreadProcessId(hwnd, &processID)
	return snapshot{hwnd: uintptr(hwnd), pid: processID, threadID: threadID, hkl: uintptr(windows.GetKeyboardLayout(threadID))}
}

func stableLayoutID(hkl uintptr) string {
	return fmt.Sprintf("HKL-%08X", uint32(hkl))
}

func readEnter() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		close(done)
	}()
	return done
}

func timestamp() string {
	return time.Now().Format(time.RFC3339Nano)
}

func win32Error(operation string, callErr error) error {
	if callErr == nil || errors.Is(callErr, windows.ERROR_SUCCESS) {
		return fmt.Errorf("%s failed", operation)
	}
	return fmt.Errorf("%s: %w", operation, callErr)
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type point struct{ X, Y int32 }

type msg struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}
