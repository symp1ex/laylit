package windowslayouts

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"laylit/internal/layouts"
)

type layoutSource string

const (
	layoutSourceTSF        layoutSource = "tsf"
	layoutSourceFocused    layoutSource = "focused-thread"
	layoutSourceForeground layoutSource = "foreground-thread"
	layoutSourceLastValid  layoutSource = "last-valid"
)

type inputContext struct {
	foregroundHWND uintptr
	foregroundTID  uint32
	foregroundHKL  uintptr
	focusedHWND    uintptr
	focusedTID     uint32
	focusedHKL     uintptr
}

type inputContextKey struct {
	hwnd uintptr
	tid  uint32
}

func (context inputContext) key() inputContextKey {
	if context.focusedHWND != 0 && context.focusedTID != 0 {
		return inputContextKey{hwnd: context.focusedHWND, tid: context.focusedTID}
	}
	return inputContextKey{hwnd: context.foregroundHWND, tid: context.foregroundTID}
}

type resolvedLayout struct {
	layout  layouts.Layout
	hkl     uintptr
	source  layoutSource
	context inputContext
}

type tsfLayoutState struct {
	hkl     uintptr
	context inputContextKey
}

type layoutResolver struct {
	readContext func() inputContext

	mu        sync.Mutex
	tsf       tsfLayoutState
	lastValid uintptr
}

func newLayoutResolver(readContext func() inputContext) *layoutResolver {
	return &layoutResolver{readContext: readContext}
}

func (resolver *layoutResolver) resolve() (resolvedLayout, error) {
	context := resolver.readContext()
	key := context.key()

	resolver.mu.Lock()
	defer resolver.mu.Unlock()

	hkl, source := uintptr(0), layoutSource("")
	switch {
	case resolver.tsf.hkl != 0 && resolver.tsf.context == key:
		hkl, source = resolver.tsf.hkl, layoutSourceTSF
	case context.focusedHKL != 0:
		hkl, source = context.focusedHKL, layoutSourceFocused
	case context.foregroundHKL != 0:
		hkl, source = context.foregroundHKL, layoutSourceForeground
	case resolver.lastValid != 0:
		hkl, source = resolver.lastValid, layoutSourceLastValid
	default:
		return resolvedLayout{}, errors.New("cannot determine active layout: no valid TSF, focused-thread, foreground-thread, or previous HKL")
	}
	resolver.lastValid = hkl
	return resolvedLayout{layout: describeHKL(hkl), hkl: hkl, source: source, context: context}, nil
}

func (resolver *layoutResolver) activateTSF(profileType, flags uint32, hkl uintptr) (resolvedLayout, bool) {
	if profileType != tfProfileTypeKeyboardLayout || flags&tfIPSinkFlagActive == 0 || hkl == 0 {
		return resolvedLayout{}, false
	}
	context := resolver.readContext()
	key := context.key()

	resolver.mu.Lock()
	resolver.tsf = tsfLayoutState{hkl: hkl, context: key}
	resolver.lastValid = hkl
	resolver.mu.Unlock()
	return resolvedLayout{layout: describeHKL(hkl), hkl: hkl, source: layoutSourceTSF, context: context}, true
}

func (resolver *layoutResolver) clearTSF() {
	resolver.mu.Lock()
	resolver.tsf = tsfLayoutState{}
	resolver.mu.Unlock()
}

func readWindowsInputContext() inputContext {
	foregroundHWND := windows.GetForegroundWindow()
	if foregroundHWND == 0 {
		return inputContext{}
	}
	foregroundTID, _ := windows.GetWindowThreadProcessId(foregroundHWND, nil)
	context := inputContext{
		foregroundHWND: uintptr(foregroundHWND),
		foregroundTID:  foregroundTID,
	}
	if foregroundTID == 0 {
		return context
	}
	context.foregroundHKL = uintptr(windows.GetKeyboardLayout(foregroundTID))
	info := guiThreadInfo{CbSize: uint32(unsafe.Sizeof(guiThreadInfo{}))}
	result, _, _ := procGetGUIThreadInfo.Call(uintptr(foregroundTID), uintptr(unsafe.Pointer(&info)))
	if result == 0 || info.HwndFocus == 0 {
		return context
	}
	focusedTID, _ := windows.GetWindowThreadProcessId(windows.HWND(info.HwndFocus), nil)
	context.focusedHWND = info.HwndFocus
	context.focusedTID = focusedTID
	if focusedTID != 0 {
		context.focusedHKL = uintptr(windows.GetKeyboardLayout(focusedTID))
	}
	return context
}

func formatResolvedLayout(resolved resolvedLayout) string {
	return fmt.Sprintf(
		"resolved source=%s layout_id=%s hkl=0x%X focused_hwnd=0x%X focused_tid=%d focused_hkl=0x%X foreground_hwnd=0x%X foreground_tid=%d foreground_hkl=0x%X",
		resolved.source, resolved.layout.ID, resolved.hkl, resolved.context.focusedHWND, resolved.context.focusedTID,
		resolved.context.focusedHKL, resolved.context.foregroundHWND, resolved.context.foregroundTID, resolved.context.foregroundHKL,
	)
}

type guiThreadInfo struct {
	CbSize        uint32
	Flags         uint32
	HwndActive    uintptr
	HwndFocus     uintptr
	HwndCapture   uintptr
	HwndMenuOwner uintptr
	HwndMoveSize  uintptr
	HwndCaret     uintptr
	RcCaret       rect
}

type rect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}
