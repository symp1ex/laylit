package keyboard

import "testing"

func TestBuildStaticReport(t *testing.T) {
	got := buildStaticReport(0x12, 0x34, 0x56, brightnessHighest)
	var want [reportSize]byte
	copy(want[:], []byte{
		0x04, 0xB4, 0x00, 0x06, 0x08, 0x00, 0x00, 0x00,
		0x06, 0x04, 0x00, 0x00, 0x00, 0x12, 0x34, 0x56,
	})

	if got != want {
		t.Fatalf("buildStaticReport() =\n% X\nwant\n% X", got, want)
	}
}

func TestBuildStaticReportChecksumOverflow(t *testing.T) {
	got := buildStaticReport(0xFF, 0xFF, 0xFF, brightnessHighest)
	if got[1] != 0x15 || got[2] != 0x03 {
		t.Fatalf("checksum = %02X %02X, want 15 03", got[1], got[2])
	}
}

func TestBuildOffReport(t *testing.T) {
	got := buildStaticReport(0, 0, 0, brightnessOff)
	if got[1] != 0x14 || got[2] != 0x00 || got[8] != modeStatic || got[9] != brightnessOff {
		t.Fatalf("off report has unexpected bytes: % X", got)
	}
}
