package windowslayouts

import (
	"errors"
	"sync/atomic"
	"syscall"
	"unsafe"
)

const (
	whKeyboardLL = 13
	hcAction     = 0

	wmKeyDown    = 0x0100
	wmKeyUp      = 0x0101
	wmSysKeyDown = 0x0104
	wmSysKeyUp   = 0x0105

	vkShift  = 0x10
	vkMenu   = 0x12
	vkLShift = 0xA0
	vkRShift = 0xA1
	vkLMenu  = 0xA4
	vkRMenu  = 0xA5
)

var (
	procSetWindowsHookExW      = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx    = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx         = user32.NewProc("CallNextHookEx")
	keyboardProcedure          = syscall.NewCallback(lowLevelKeyboardProc)
	activeKeyboardSubscription atomic.Pointer[subscription]
)

func installKeyboardHook(subscription *subscription) (uintptr, error) {
	if !activeKeyboardSubscription.CompareAndSwap(nil, subscription) {
		return 0, errors.New("a Windows layout keyboard listener is already active")
	}
	hook, _, callErr := procSetWindowsHookExW.Call(whKeyboardLL, keyboardProcedure, 0, 0)
	if hook == 0 {
		activeKeyboardSubscription.CompareAndSwap(subscription, nil)
		return 0, win32Error("SetWindowsHookExW(WH_KEYBOARD_LL)", callErr)
	}
	return hook, nil
}

func uninstallKeyboardHook(subscription *subscription, hook uintptr) error {
	result, _, callErr := procUnhookWindowsHookEx.Call(hook)
	activeKeyboardSubscription.CompareAndSwap(subscription, nil)
	if result == 0 {
		return win32Error("UnhookWindowsHookEx", callErr)
	}
	return nil
}

// The low-level hook is a trigger, not a source of layout identity. It observes
// only the Alt/Shift chord, never suppresses input, and asks the listener window
// to re-read the foreground thread's actual HKL after both modifiers are up.
// Runtime traces show that this completed-chord point is when Alt+Shift's new
// HKL is visible on Windows 11.
func lowLevelKeyboardProc(code int, message uintptr, data *keyboardHookData) uintptr {
	subscription := activeKeyboardSubscription.Load()
	if subscription != nil && code == hcAction && data != nil && subscription.keyboard.observe(data.VirtualKey, message) {
		posted, _, callErr := procPostMessageW.Call(subscription.hwnd, wmKeyboardChordCompleted, uintptr(data.VirtualKey), message)
		if posted == 0 {
			err := win32Error("PostMessage(keyboard chord completed)", callErr)
			subscription.setLoopError(err)
			replaceError(subscription.errors, err)
		}
	}
	result, _, _ := procCallNextHookEx.Call(0, uintptr(code), message, uintptr(unsafe.Pointer(data)))
	return result
}

type keyboardChordState struct {
	altDown   bool
	shiftDown bool
	chordSeen bool
}

func (state *keyboardChordState) observe(virtualKey uint32, message uintptr) bool {
	modifier := modifierForVirtualKey(virtualKey)
	if modifier == modifierNone {
		return false
	}
	down := message == wmKeyDown || message == wmSysKeyDown
	up := message == wmKeyUp || message == wmSysKeyUp
	if !down && !up {
		return false
	}
	switch modifier {
	case modifierAlt:
		state.altDown = down
	case modifierShift:
		state.shiftDown = down
	}
	if state.altDown && state.shiftDown {
		state.chordSeen = true
	}
	if !up || !state.chordSeen || state.altDown || state.shiftDown {
		return false
	}
	state.chordSeen = false
	return true
}

type keyboardModifier uint8

const (
	modifierNone keyboardModifier = iota
	modifierAlt
	modifierShift
)

func modifierForVirtualKey(virtualKey uint32) keyboardModifier {
	switch virtualKey {
	case vkMenu, vkLMenu, vkRMenu:
		return modifierAlt
	case vkShift, vkLShift, vkRShift:
		return modifierShift
	default:
		return modifierNone
	}
}

type keyboardHookData struct {
	VirtualKey uint32
	ScanCode   uint32
	Flags      uint32
	Time       uint32
	ExtraInfo  uintptr
}
