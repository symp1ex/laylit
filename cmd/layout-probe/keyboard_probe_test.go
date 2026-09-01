package main

import "testing"

func TestKeyboardProbeSignalsBothReleasesAfterAltShiftChord(t *testing.T) {
	probe := &keyboardProbe{}

	sequence := []struct {
		virtualKey uint32
		message    uintptr
		wantSignal bool
	}{
		{virtualKey: vkLMenu, message: wmSysKeyDown},
		{virtualKey: vkLShift, message: wmKeyDown},
		{virtualKey: vkLShift, message: wmKeyUp, wantSignal: true},
		{virtualKey: vkLMenu, message: wmSysKeyUp, wantSignal: true},
	}
	for index, event := range sequence {
		if got := probe.observeModifier(event.virtualKey, event.message); got != event.wantSignal {
			t.Fatalf("event %d: signal=%t, want %t", index, got, event.wantSignal)
		}
	}
	if probe.chordSeen {
		t.Fatal("completed chord remained active")
	}
}

func TestKeyboardProbeIgnoresSingleModifierAndOrdinaryKeys(t *testing.T) {
	probe := &keyboardProbe{}
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
		if probe.observeModifier(event.virtualKey, event.message) {
			t.Fatalf("event %d unexpectedly signaled", index)
		}
	}
}
