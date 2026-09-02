package windowslayouts

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"laylit/internal/layouts"
)

func TestStableLayoutIDUsesAllLow32Bits(t *testing.T) {
	if got := stableLayoutID(uintptr(0x1234567804090409)); got != "HKL-04090409" {
		t.Fatalf("stableLayoutID() = %q", got)
	}
}

func TestDescribeHKLFallbackHasStableIdentifier(t *testing.T) {
	layout := describeHKL(uintptr(0x04090409))
	if layout.ID != "HKL-04090409" || layout.Name == "" {
		t.Fatalf("describeHKL() = %#v", layout)
	}
}

func TestDeliveredShellAndInputLanguageMessagesRequestResynchronization(t *testing.T) {
	const shellMessage = 0xC028
	for _, test := range []struct {
		name    string
		message uint32
		want    bool
	}{
		{name: "registered Shell message", message: shellMessage, want: true},
		{name: "input language change", message: wmInputLangChange, want: true},
		{name: "completed keyboard chord", message: wmKeyboardChordCompleted, want: true},
		{name: "unrelated window message", message: 0x0400, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isLayoutResynchronizationMessage(test.message, shellMessage); got != test.want {
				t.Fatalf("isLayoutResynchronizationMessage(0x%X) = %t, want %t", test.message, got, test.want)
			}
		})
	}
}

func TestKeyboardChordSignalsWhenAltShiftIsFullyReleased(t *testing.T) {
	state := &keyboardChordState{}
	sequence := []struct {
		virtualKey uint32
		message    uintptr
		wantSignal bool
	}{
		{virtualKey: vkLMenu, message: wmSysKeyDown},
		{virtualKey: vkLShift, message: wmKeyDown},
		{virtualKey: vkLMenu, message: wmKeyUp},
		{virtualKey: vkLShift, message: wmKeyUp, wantSignal: true},
	}
	for index, event := range sequence {
		if got := state.observe(event.virtualKey, event.message); got != event.wantSignal {
			t.Fatalf("event %d: signal=%t, want %t", index, got, event.wantSignal)
		}
	}
	if state.chordSeen {
		t.Fatal("completed chord remained active")
	}
}

func TestKeyboardChordIgnoresSingleModifierAndOrdinaryKeys(t *testing.T) {
	state := &keyboardChordState{}
	sequence := []struct {
		virtualKey uint32
		message    uintptr
	}{
		{virtualKey: vkLShift, message: wmKeyDown},
		{virtualKey: 'A', message: wmKeyDown},
		{virtualKey: 'A', message: wmKeyUp},
		{virtualKey: vkLShift, message: wmKeyUp},
	}
	for index, event := range sequence {
		if state.observe(event.virtualKey, event.message) {
			t.Fatalf("event %d unexpectedly signaled", index)
		}
	}
}

func TestPublishCurrentDeduplicatesAndKeepsNewestPendingLayout(t *testing.T) {
	currents := []layouts.Layout{
		{ID: "en", Name: "English"},
		{ID: "en", Name: "English"},
		{ID: "ru", Name: "Russian"},
		{ID: "de", Name: "German"},
	}
	currentIndex := 0
	var diagnostics []string
	subscription := &subscription{
		current: func(context.Context) (layouts.Layout, error) {
			current := currents[currentIndex]
			currentIndex++
			return current, nil
		},
		events: make(chan layouts.Layout, 1),
		errors: make(chan error, 1),
		debugf: func(format string, args ...any) {
			diagnostics = append(diagnostics, fmt.Sprintf(format, args...))
		},
	}

	subscription.publishCurrent()
	if got := <-subscription.events; got.ID != "en" {
		t.Fatalf("first published layout = %q, want en", got.ID)
	}

	subscription.publishCurrent()
	select {
	case duplicate := <-subscription.events:
		t.Fatalf("duplicate layout was published: %#v", duplicate)
	default:
	}

	subscription.publishCurrent()
	subscription.publishCurrent()
	if got := <-subscription.events; got.ID != "de" {
		t.Fatalf("pending layout = %q, want newest de", got.ID)
	}
	trace := strings.Join(diagnostics, "\n")
	if !strings.Contains(trace, "layout publish layout_id=en result=deduplicated") || !strings.Contains(trace, "layout publish layout_id=de result=published") {
		t.Fatalf("diagnostic trace missing publish decisions:\n%s", trace)
	}
}

func TestResolverUsesFocusedThenForegroundThreadLayout(t *testing.T) {
	context := inputContext{
		foregroundHWND: 1, foregroundTID: 10, foregroundHKL: 0x04090409,
		focusedHWND: 2, focusedTID: 20, focusedHKL: 0x04190419,
	}
	resolver := newLayoutResolver(func() inputContext { return context })

	resolved, err := resolver.resolve()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.layout.ID != "HKL-04190419" || resolved.source != layoutSourceFocused {
		t.Fatalf("focused resolution = %#v", resolved)
	}

	context.focusedHKL = 0
	resolved, err = resolver.resolve()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.layout.ID != "HKL-04090409" || resolved.source != layoutSourceForeground {
		t.Fatalf("foreground resolution = %#v", resolved)
	}
}

func TestActiveTSFKeyboardProfileUpdatesStateAndPublishes(t *testing.T) {
	context := inputContext{foregroundHWND: 1, foregroundTID: 10, foregroundHKL: 0x04090409}
	resolver := newLayoutResolver(func() inputContext { return context })
	subscription := &subscription{
		resolver: resolver,
		events:   make(chan layouts.Layout, 1),
		errors:   make(chan error, 1),
	}

	subscription.onTSFProfile(tfProfileTypeKeyboardLayout, tfIPSinkFlagActive, 0x04190419)
	if got := <-subscription.events; got.ID != "HKL-04190419" {
		t.Fatalf("TSF event layout = %q", got.ID)
	}
	resolved, err := resolver.resolve()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.layout.ID != "HKL-04190419" || resolved.source != layoutSourceTSF {
		t.Fatalf("TSF resolution = %#v", resolved)
	}
}

func TestInvalidTSFCallbacksDoNotReplaceValidState(t *testing.T) {
	context := inputContext{foregroundHWND: 1, foregroundTID: 10, foregroundHKL: 0x04090409}
	resolver := newLayoutResolver(func() inputContext { return context })
	if _, accepted := resolver.activateTSF(tfProfileTypeKeyboardLayout, tfIPSinkFlagActive, 0x04190419); !accepted {
		t.Fatal("valid TSF activation was rejected")
	}

	for _, test := range []struct {
		name        string
		profileType uint32
		flags       uint32
		hkl         uintptr
	}{
		{name: "inactive", profileType: tfProfileTypeKeyboardLayout, hkl: 0x04090409},
		{name: "not keyboard layout", profileType: 1, flags: tfIPSinkFlagActive, hkl: 0x04090409},
		{name: "zero HKL", profileType: tfProfileTypeKeyboardLayout, flags: tfIPSinkFlagActive},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, accepted := resolver.activateTSF(test.profileType, test.flags, test.hkl); accepted {
				t.Fatal("invalid TSF activation was accepted")
			}
			resolved, err := resolver.resolve()
			if err != nil {
				t.Fatal(err)
			}
			if resolved.layout.ID != "HKL-04190419" || resolved.source != layoutSourceTSF {
				t.Fatalf("state after invalid callback = %#v", resolved)
			}
		})
	}
}

func TestResolverUsesLastValidWhenWindowsLayoutsAreTemporarilyZero(t *testing.T) {
	context := inputContext{foregroundHWND: 1, foregroundTID: 10, foregroundHKL: 0x04190419}
	resolver := newLayoutResolver(func() inputContext { return context })
	if _, err := resolver.resolve(); err != nil {
		t.Fatal(err)
	}
	context.foregroundHKL = 0

	resolved, err := resolver.resolve()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.layout.ID != "HKL-04190419" || resolved.source != layoutSourceLastValid {
		t.Fatalf("zero-HKL fallback = %#v", resolved)
	}
}

func TestForegroundTransitionResynchronizesAndDoesNotReuseOldContextTSF(t *testing.T) {
	context := inputContext{foregroundHWND: 1, foregroundTID: 10, foregroundHKL: 0x04190419}
	resolver := newLayoutResolver(func() inputContext { return context })
	if _, accepted := resolver.activateTSF(tfProfileTypeKeyboardLayout, tfIPSinkFlagActive, 0x04190419); !accepted {
		t.Fatal("valid TSF activation was rejected")
	}

	context = inputContext{
		foregroundHWND: 2, foregroundTID: 20, foregroundHKL: 0x04090409,
		focusedHWND: 3, focusedTID: 30, focusedHKL: 0x04090409,
	}
	resolved, err := resolver.resolve()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.layout.ID != "HKL-04090409" || resolved.source != layoutSourceFocused {
		t.Fatalf("foreground transition resolution = %#v", resolved)
	}
}

func TestFreshTSFWinsOverStaleWindowsReadInSameContext(t *testing.T) {
	context := inputContext{foregroundHWND: 1, foregroundTID: 10, foregroundHKL: 0x04090409}
	resolver := newLayoutResolver(func() inputContext { return context })
	if _, accepted := resolver.activateTSF(tfProfileTypeKeyboardLayout, tfIPSinkFlagActive, 0x04190419); !accepted {
		t.Fatal("valid TSF activation was rejected")
	}

	resolved, err := resolver.resolve()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.layout.ID != "HKL-04190419" || resolved.source != layoutSourceTSF {
		t.Fatalf("stale Windows read overrode TSF: %#v", resolved)
	}
}

func TestTSFThenKeyboardTriggerDoesNotRollBackOrDuplicate(t *testing.T) {
	snapshot := inputContext{foregroundHWND: 1, foregroundTID: 10, foregroundHKL: 0x04090409}
	resolver := newLayoutResolver(func() inputContext { return snapshot })
	subscription := &subscription{
		resolver: resolver,
		current: func(context.Context) (layouts.Layout, error) {
			resolved, err := resolver.resolve()
			return resolved.layout, err
		},
		events: make(chan layouts.Layout, 1),
		errors: make(chan error, 1),
	}

	subscription.onTSFProfile(tfProfileTypeKeyboardLayout, tfIPSinkFlagActive, 0x04190419)
	if got := <-subscription.events; got.ID != "HKL-04190419" {
		t.Fatalf("TSF event layout = %q", got.ID)
	}
	subscription.handleLayoutNotification(wmKeyboardChordCompleted, 0xC028)
	select {
	case event := <-subscription.events:
		t.Fatalf("stale keyboard trigger published %#v", event)
	default:
	}
}

func TestZeroWindowsReadDoesNotPublishFalseLayout(t *testing.T) {
	snapshot := inputContext{foregroundHWND: 1, foregroundTID: 10, foregroundHKL: 0x04190419}
	resolver := newLayoutResolver(func() inputContext { return snapshot })
	subscription := &subscription{
		current: func(context.Context) (layouts.Layout, error) {
			resolved, err := resolver.resolve()
			return resolved.layout, err
		},
		events: make(chan layouts.Layout, 1),
		errors: make(chan error, 1),
	}
	subscription.publishCurrent()
	<-subscription.events
	snapshot.foregroundHKL = 0

	subscription.publishCurrent()
	select {
	case event := <-subscription.events:
		t.Fatalf("zero Windows HKL published %#v", event)
	default:
	}
	select {
	case err := <-subscription.errors:
		t.Fatalf("zero Windows HKL reported an error despite fallback: %v", err)
	default:
	}
}

func TestKeyboardChordTriggerResolvesAndPublishesWithoutForegroundChange(t *testing.T) {
	calls := 0
	subscription := &subscription{
		current: func(context.Context) (layouts.Layout, error) {
			calls++
			return layouts.Layout{ID: "ru", Name: "Russian"}, nil
		},
		events: make(chan layouts.Layout, 1),
		errors: make(chan error, 1),
	}

	if !subscription.handleLayoutNotification(wmKeyboardChordCompleted, 0xC028) {
		t.Fatal("keyboard chord was not handled")
	}
	if calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", calls)
	}
	if got := <-subscription.events; got.ID != "ru" {
		t.Fatalf("published layout = %q", got.ID)
	}
}

func TestStoppedSubscriptionIgnoresTSFCallbacks(t *testing.T) {
	context := inputContext{foregroundHWND: 1, foregroundTID: 10, foregroundHKL: 0x04090409}
	resolver := newLayoutResolver(func() inputContext { return context })
	subscription := &subscription{
		resolver: resolver,
		events:   make(chan layouts.Layout, 1),
		errors:   make(chan error, 1),
	}
	subscription.stopPublishing()
	subscription.onTSFProfile(tfProfileTypeKeyboardLayout, tfIPSinkFlagActive, 0x04190419)

	select {
	case event := <-subscription.events:
		t.Fatalf("event after shutdown = %#v", event)
	default:
	}
	resolved, err := resolver.resolve()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.layout.ID != "HKL-04090409" || resolved.source != layoutSourceForeground {
		t.Fatalf("TSF state changed after shutdown: %#v", resolved)
	}
}

func TestInactiveTSFSinkDoesNotInvokeCallback(t *testing.T) {
	calls := 0
	sink := newProfileActivationSink(func(uint32, uint32, uintptr) { calls++ })
	sink.active.Store(false)
	profileActivationOnActivated(
		unsafe.Pointer(sink), tfProfileTypeKeyboardLayout, 0, nil, nil, nil, 0x04190419, tfIPSinkFlagActive,
	)
	if calls != 0 {
		t.Fatalf("callbacks after sink shutdown = %d", calls)
	}
	profileActivationRelease(unsafe.Pointer(sink))
}

func TestConcurrentTSFCallbackAndShutdownLeaveNoAuthoritativeState(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		context := inputContext{foregroundHWND: 1, foregroundTID: 10, foregroundHKL: 0x04090409}
		resolver := newLayoutResolver(func() inputContext { return context })
		subscription := &subscription{
			resolver: resolver,
			events:   make(chan layouts.Layout, 1),
			errors:   make(chan error, 1),
		}
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			subscription.onTSFProfile(tfProfileTypeKeyboardLayout, tfIPSinkFlagActive, 0x04190419)
		}()
		go func() {
			defer wait.Done()
			subscription.stopPublishing()
		}()
		wait.Wait()

		resolved, err := resolver.resolve()
		if err != nil {
			t.Fatal(err)
		}
		if resolved.layout.ID != "HKL-04090409" || resolved.source != layoutSourceForeground {
			t.Fatalf("iteration %d retained TSF after shutdown: %#v", iteration, resolved)
		}
	}
}

func TestTSFListenerLifecycleIntegration(t *testing.T) {
	if os.Getenv("LAYLIT_TSF_INTEGRATION") != "1" {
		t.Skip("set LAYLIT_TSF_INTEGRATION=1 in an interactive Windows session")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	listener, err := startTSFListener(func(uint32, uint32, uintptr) {})
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.close(); err != nil {
		t.Fatal(err)
	}
}
