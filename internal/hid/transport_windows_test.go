package hid

import "testing"

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
