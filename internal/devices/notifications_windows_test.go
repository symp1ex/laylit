package devices

import (
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsNotificationsSubscribeWithoutExactATKInstance(t *testing.T) {
	sub, err := SubscribeWindows()
	if err != nil {
		t.Fatal(err)
	}
	if sub.handles[0] == 0 || sub.handles[1] == 0 {
		t.Error("missing HID or device-instance notification handle")
	}
	if err := sub.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceNotificationFiltersDeviceInstances(t *testing.T) {
	key := notificationSequence.Add(1)
	events := make(chan struct{}, 1)
	notificationChannels.Store(key, events)
	defer notificationChannels.Delete(key)

	for _, test := range []struct {
		filter uint32
		id     string
		want   bool
	}{
		{filter: 0, want: true}, // The HID subscription filters by interface class.
		{filter: notifyDeviceInstance, id: strings.ToLower(ATKInstanceID), want: true},
		{filter: notifyDeviceInstance, id: `ACPI\PNP0C14\OTHER`},
		{filter: notifyDeviceInstance},
	} {
		raw := struct {
			filterType uint32
			reserved   uint32
			instanceID [maxDeviceIDLength]uint16
		}{filterType: test.filter}
		id, err := windows.UTF16FromString(test.id)
		if err != nil {
			t.Fatal(err)
		}
		copy(raw.instanceID[:], id)
		size := unsafe.Offsetof(raw.instanceID) + uintptr(len(id)*2)
		deviceNotification(0, key, 0, (*notificationEventData)(unsafe.Pointer(&raw)), size)
		var notified bool
		select {
		case <-events:
			notified = true
		default:
		}
		if notified != test.want {
			t.Errorf("filter %d instance %q: notified = %t, want %t", test.filter, test.id, notified, test.want)
		}
	}
}
