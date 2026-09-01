package keyboard

import "testing"

func TestRGBCandidate(t *testing.T) {
	tests := []struct {
		name string
		info Info
		want bool
	}{
		{"confirmed selector", Info{Interface: 1, UsagePage: 0xFF1C, Usage: 1}, true},
		{"keyboard input interface", Info{Interface: 0, UsagePage: 0x0001, Usage: 0x0006}, false},
		{"wrong usage page", Info{Interface: 1, UsagePage: 0x0001, Usage: 0x0006}, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.info.RGBCandidate(); got != test.want {
				t.Fatalf("RGBCandidate() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestInterfaceNumberFromPath(t *testing.T) {
	tests := []struct {
		path string
		want int
	}{
		{`\\?\hid#vid_320f&pid_5000&mi_01&col04#abc`, 1},
		{`\\?\HID#VID_320F&PID_5000&MI_0A#abc`, 10},
		{`\\?\hid#vid_320f&pid_5000#abc`, -1},
	}

	for _, test := range tests {
		if got := interfaceNumberFromPath(test.path); got != test.want {
			t.Errorf("interfaceNumberFromPath(%q) = %d, want %d", test.path, got, test.want)
		}
	}
}
