package windowslayouts

import "testing"

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
