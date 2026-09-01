package main

import (
	"fmt"
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
	procSetWindowsHookExW   = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	keyboardProcedure       = syscall.NewCallback(lowLevelKeyboardProc)
	activeKeyboardProbe     atomic.Pointer[keyboardProbe]
)

type keyboardProbe struct {
	state     *probeState
	hook      uintptr
	altDown   bool
	shiftDown bool
	chordSeen bool
}

func startKeyboardProbe(state *probeState) (*keyboardProbe, error) {
	probe := &keyboardProbe{state: state}
	if !activeKeyboardProbe.CompareAndSwap(nil, probe) {
		return nil, fmt.Errorf("a low-level keyboard probe is already active")
	}

	hook, _, callErr := procSetWindowsHookExW.Call(whKeyboardLL, keyboardProcedure, 0, 0)
	if hook == 0 {
		activeKeyboardProbe.CompareAndSwap(probe, nil)
		return nil, win32Error("SetWindowsHookExW(WH_KEYBOARD_LL)", callErr)
	}
	probe.hook = hook
	state.logger.Printf("timestamp=%s phase=keyboard-hook-startup result=registered hook=0x%X callback_owner_tid=%d", timestamp(), hook, state.listenerTID)
	return probe, nil
}

func (probe *keyboardProbe) close() {
	if probe.hook != 0 {
		result, _, callErr := procUnhookWindowsHookEx.Call(probe.hook)
		if result == 0 {
			probe.state.logger.Printf("timestamp=%s phase=keyboard-hook-shutdown result=error error=%q", timestamp(), win32Error("UnhookWindowsHookEx", callErr))
		} else {
			probe.state.logger.Printf("timestamp=%s phase=keyboard-hook-shutdown result=clean", timestamp())
		}
		probe.hook = 0
	}
	activeKeyboardProbe.CompareAndSwap(probe, nil)
}

func lowLevelKeyboardProc(code int, message uintptr, data *keyboardHookData) uintptr {
	probe := activeKeyboardProbe.Load()
	if probe != nil && code == hcAction && data != nil {
		if probe.observeModifier(data.VirtualKey, message) {
			procPostMessageW.Call(probe.state.hwnd, wmKeyboardChordCompleted, uintptr(data.VirtualKey), message)
		}
	}

	result, _, _ := procCallNextHookEx.Call(0, uintptr(code), message, uintptr(unsafe.Pointer(data)))
	return result
}

func (probe *keyboardProbe) observeModifier(virtualKey uint32, message uintptr) bool {
	modifier := modifierForVirtualKey(virtualKey)
	if modifier == modifierNone {
		return false
	}
	down := message == wmKeyDown || message == wmSysKeyDown
	up := message == wmKeyUp || message == wmSysKeyUp
	if !down && !up {
		return false
	}

	probe.setModifier(modifier, down)
	if probe.altDown && probe.shiftDown {
		probe.chordSeen = true
	}
	if !up || !probe.chordSeen {
		return false
	}
	if !probe.altDown && !probe.shiftDown {
		probe.chordSeen = false
	}
	return true
}

func (probe *keyboardProbe) setModifier(modifier keyboardModifier, down bool) {
	switch modifier {
	case modifierAlt:
		probe.altDown = down
	case modifierShift:
		probe.shiftDown = down
	}
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
