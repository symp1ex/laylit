package windowslayouts

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"evision-rgb/internal/layouts"
	"golang.org/x/sys/windows"
)

const (
	wmClose               = 0x0010
	wmDestroy             = 0x0002
	wmInputLangChange     = 0x0051
	hshellWindowActivated = 4
	hshellLanguage        = 8
	hshelLrudeActivated   = 0x8004
	localeSLocalizedName  = 0x00000002
)

var (
	user32                        = windows.NewLazySystemDLL("user32.dll")
	kernel32                      = windows.NewLazySystemDLL("kernel32.dll")
	procGetKeyboardLayoutList     = user32.NewProc("GetKeyboardLayoutList")
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
	procLCIDToLocaleName          = kernel32.NewProc("LCIDToLocaleName")
	procGetLocaleInfoEx           = kernel32.NewProc("GetLocaleInfoEx")
	procGetModuleHandleW          = kernel32.NewProc("GetModuleHandleW")
	shellHookMessageName, _       = windows.UTF16PtrFromString("SHELLHOOK")
	windowProcedure               = syscall.NewCallback(windowProc)
	windowSubscriptions           sync.Map
	windowClassSequence           atomic.Uint64
)

type Source struct{}

func NewSource() *Source { return &Source{} }

func (source *Source) List(ctx context.Context) ([]layouts.Layout, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	count, _, callErr := procGetKeyboardLayoutList.Call(0, 0)
	if count == 0 {
		return nil, win32Error("GetKeyboardLayoutList(size)", callErr)
	}
	handles := make([]windows.Handle, int(count))
	copied, _, callErr := procGetKeyboardLayoutList.Call(count, uintptr(unsafe.Pointer(&handles[0])))
	if copied == 0 {
		return nil, win32Error("GetKeyboardLayoutList(data)", callErr)
	}
	if copied > count {
		copied = count
	}

	seen := make(map[string]struct{}, int(copied))
	result := make([]layouts.Layout, 0, int(copied))
	for _, handle := range handles[:int(copied)] {
		layout := describeHKL(uintptr(handle))
		if _, exists := seen[layout.ID]; exists {
			continue
		}
		seen[layout.ID] = struct{}{}
		result = append(result, layout)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// Current deliberately queries the foreground window's owning thread. Calling
// GetKeyboardLayout(0) here would return the background listener's own layout.
func (source *Source) Current(ctx context.Context) (layouts.Layout, error) {
	if err := ctx.Err(); err != nil {
		return layouts.Layout{}, err
	}
	hwnd := windows.GetForegroundWindow()
	if hwnd == 0 {
		return layouts.Layout{}, errors.New("cannot determine active layout: no foreground window")
	}
	threadID, err := windows.GetWindowThreadProcessId(hwnd, nil)
	if threadID == 0 {
		return layouts.Layout{}, fmt.Errorf("cannot determine foreground window thread: %w", err)
	}
	hkl := windows.GetKeyboardLayout(threadID)
	if hkl == 0 {
		return layouts.Layout{}, errors.New("GetKeyboardLayout returned an empty input locale identifier")
	}
	return describeHKL(uintptr(hkl)), nil
}

func (source *Source) Subscribe(ctx context.Context) (layouts.Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	subscription := &subscription{
		source: source, events: make(chan layouts.Layout, 1), errors: make(chan error, 1),
		done: make(chan struct{}),
	}
	ready := make(chan error, 1)
	go subscription.messageLoop(ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	go func() {
		select {
		case <-ctx.Done():
			_ = subscription.Close()
		case <-subscription.done:
		}
	}()
	return subscription, nil
}

type subscription struct {
	source    *Source
	events    chan layouts.Layout
	errors    chan error
	done      chan struct{}
	hwnd      uintptr
	closeOnce sync.Once
	mu        sync.Mutex
	loopErr   error
	lastID    string
}

func (subscription *subscription) Events() <-chan layouts.Layout { return subscription.events }
func (subscription *subscription) Errors() <-chan error          { return subscription.errors }

func (subscription *subscription) Close() error {
	subscription.closeOnce.Do(func() {
		if subscription.hwnd != 0 {
			ok, _, callErr := procPostMessageW.Call(subscription.hwnd, wmClose, 0, 0)
			if ok == 0 {
				subscription.setLoopError(win32Error("PostMessage(WM_CLOSE)", callErr))
			}
		}
		<-subscription.done
	})
	subscription.mu.Lock()
	defer subscription.mu.Unlock()
	return subscription.loopErr
}

func (subscription *subscription) messageLoop(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	className, _ := windows.UTF16PtrFromString(fmt.Sprintf("EVisionRGB.LayoutEvents.%d", windowClassSequence.Add(1)))
	module, _, callErr := procGetModuleHandleW.Call(0)
	if module == 0 {
		ready <- win32Error("GetModuleHandle", callErr)
		close(subscription.done)
		return
	}
	class := wndClassEx{CbSize: uint32(unsafe.Sizeof(wndClassEx{})), LpfnWndProc: windowProcedure, HInstance: module, LpszClassName: className}
	atom, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		ready <- win32Error("RegisterClassEx", callErr)
		close(subscription.done)
		return
	}
	defer procUnregisterClassW.Call(uintptr(unsafe.Pointer(className)), module)

	hwnd, _, callErr := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(className)), 0, 0, 0, 0, 0, 0, 0, module, 0)
	if hwnd == 0 {
		ready <- win32Error("CreateWindowEx", callErr)
		close(subscription.done)
		return
	}
	subscription.hwnd = hwnd
	windowSubscriptions.Store(hwnd, subscription)
	defer windowSubscriptions.Delete(hwnd)

	shellMessage, _, callErr := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(shellHookMessageName)))
	if shellMessage == 0 {
		procDestroyWindow.Call(hwnd)
		ready <- win32Error("RegisterWindowMessage(SHELLHOOK)", callErr)
		close(subscription.done)
		return
	}
	registered, _, callErr := procRegisterShellHookWindow.Call(hwnd)
	if registered == 0 {
		procDestroyWindow.Call(hwnd)
		ready <- win32Error("RegisterShellHookWindow", callErr)
		close(subscription.done)
		return
	}
	defer procDeregisterShellHookWindow.Call(hwnd)

	windowSubscriptions.Store(hwnd, windowState{subscription: subscription, shellMessage: uint32(shellMessage)})
	ready <- nil

	var message msg
	for {
		result, _, callErr := procGetMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(result) == -1 {
			subscription.setLoopError(win32Error("GetMessage", callErr))
			break
		}
		if result == 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
	}
	close(subscription.events)
	close(subscription.errors)
	close(subscription.done)
}

func (subscription *subscription) publishCurrent() {
	current, err := subscription.source.Current(context.Background())
	if err != nil {
		replaceError(subscription.errors, fmt.Errorf("read active layout after Windows notification: %w", err))
		return
	}
	if current.ID == subscription.lastID {
		return
	}
	subscription.lastID = current.ID
	replaceLayout(subscription.events, current)
}

func (subscription *subscription) setLoopError(err error) {
	subscription.mu.Lock()
	defer subscription.mu.Unlock()
	if subscription.loopErr == nil {
		subscription.loopErr = err
	}
}

type windowState struct {
	subscription *subscription
	shellMessage uint32
}

func windowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	if value, ok := windowSubscriptions.Load(hwnd); ok {
		state, stateOK := value.(windowState)
		if stateOK && message == state.shellMessage {
			switch wParam {
			case hshellLanguage, hshellWindowActivated, hshelLrudeActivated:
				state.subscription.publishCurrent()
			}
		}
		if stateOK && message == wmInputLangChange {
			state.subscription.publishCurrent()
			return 1
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

func describeHKL(hkl uintptr) layouts.Layout {
	id := stableLayoutID(hkl)
	name := localizedLanguageName(uint16(hkl))
	if name == "" {
		name = "Windows input layout " + id
	}
	return layouts.Layout{ID: id, Name: name}
}

// HKL values are pointer-sized handles, but the documented input-locale
// identifier is carried in the low 32 bits. Keeping all eight hexadecimal
// digits distinguishes physical layout variants that share a language ID.
func stableLayoutID(hkl uintptr) string {
	return fmt.Sprintf("HKL-%08X", uint32(hkl))
}

func localizedLanguageName(languageID uint16) string {
	localeName := make([]uint16, 85)
	count, _, _ := procLCIDToLocaleName.Call(uintptr(languageID), uintptr(unsafe.Pointer(&localeName[0])), uintptr(len(localeName)), 0)
	if count == 0 {
		return ""
	}
	displayName := make([]uint16, 128)
	count, _, _ = procGetLocaleInfoEx.Call(uintptr(unsafe.Pointer(&localeName[0])), localeSLocalizedName, uintptr(unsafe.Pointer(&displayName[0])), uintptr(len(displayName)))
	if count == 0 {
		return windows.UTF16ToString(localeName)
	}
	return windows.UTF16ToString(displayName)
}

func replaceLayout(channel chan layouts.Layout, value layouts.Layout) {
	select {
	case channel <- value:
		return
	default:
	}
	select {
	case <-channel:
	default:
	}
	channel <- value
}

func replaceError(channel chan error, value error) {
	select {
	case channel <- value:
		return
	default:
	}
	select {
	case <-channel:
	default:
	}
	channel <- value
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
	HWnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}
