package windowslayouts

import (
	"context"
	"fmt"
	"strings"
	"testing"

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
