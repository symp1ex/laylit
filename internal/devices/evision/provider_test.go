package evision

import (
	"testing"

	"evision-rgb/internal/hid"
)

func TestRGBCandidateDoesNotConstrainUsageID(t *testing.T) {
	for _, usage := range []uint16{0, 1, 0xFFFF} {
		if !rgbCandidate(hid.Info{Interface: 1, UsagePage: 0xFF1C, Usage: usage}) {
			t.Fatalf("usage %04X was unexpectedly rejected", usage)
		}
	}
	if rgbCandidate(hid.Info{Interface: 0, UsagePage: 0xFF1C}) {
		t.Fatal("wrong interface accepted")
	}
	if rgbCandidate(hid.Info{Interface: 1, UsagePage: 1}) {
		t.Fatal("wrong usage page accepted")
	}
}

func TestSelectCandidateRefusesZeroAndAmbiguous(t *testing.T) {
	if _, err := selectCandidate(nil); err == nil {
		t.Fatal("zero candidates accepted")
	}
	candidate := hid.Info{Interface: 1, UsagePage: 0xFF1C}
	if _, err := selectCandidate([]hid.Info{candidate, candidate}); err == nil {
		t.Fatal("ambiguous candidates accepted")
	}
}
