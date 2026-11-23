package devices

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const ATKInstanceID = `ACPI\PNP0C14\ATK`

const (
	notifyAllDeviceInstances = 0x2
	notifyDeviceInstance     = 2
	maxDeviceIDLength        = 200
)

var (
	notificationDLL        = windows.NewLazySystemDLL("cfgmgr32.dll")
	registerNotification   = notificationDLL.NewProc("CM_Register_Notification")
	unregisterNotification = notificationDLL.NewProc("CM_Unregister_Notification")
	notificationCallback   = syscall.NewCallback(deviceNotification)
	notificationChannels   sync.Map
	notificationSequence   atomic.Uintptr
)

type notificationFilter struct {
	size       uint32
	flags      uint32
	filterType uint32
	reserved   uint32
	data       [maxDeviceIDLength]uint16
}

type notificationEventData struct {
	filterType uint32
	reserved   uint32
	instanceID uint16 // First element of the variable-length DeviceInstance payload.
}

func deviceNotification(_ uintptr, key uintptr, _ uintptr, event *notificationEventData, size uintptr) uintptr {
	if event == nil || size < unsafe.Offsetof(event.instanceID) {
		return 0
	}
	if event.filterType == notifyDeviceInstance {
		length := min((size-unsafe.Offsetof(event.instanceID))/2, maxDeviceIDLength)
		instanceID := windows.UTF16ToString(unsafe.Slice(&event.instanceID, length))
		if !strings.EqualFold(instanceID, ATKInstanceID) {
			return 0
		}
	}
	if entry, ok := notificationChannels.Load(key); ok {
		select {
		case entry.(chan struct{}) <- struct{}{}:
		default:
		}
	}
	return 0
}

type WindowsNotifications struct {
	events  chan struct{}
	key     uintptr
	handles [2]uintptr
}

// SubscribeWindows registers before discovery so arrivals during a scan queue
// another scan. The callback only signals the runtime.
func SubscribeWindows() (*WindowsNotifications, error) {
	sub := &WindowsNotifications{events: make(chan struct{}, 1), key: notificationSequence.Add(1)}
	notificationChannels.Store(sub.key, sub.events)
	hid := notificationFilter{}
	hid.size = uint32(unsafe.Sizeof(hid))
	// GUID_DEVINTERFACE_HID
	*(*windows.GUID)(unsafe.Pointer(&hid.data[0])) = windows.GUID{
		Data1: 0x4D1E55B2, Data2: 0xF16F, Data3: 0x11CF,
		Data4: [8]byte{0x88, 0xCB, 0x00, 0x11, 0x11, 0x00, 0x00, 0x30},
	}
	// Registering a particular instance fails if its devnode does not exist.
	// Match ATK in the callback so the same watcher works on other PCs.
	instances := notificationFilter{size: hid.size, flags: notifyAllDeviceInstances, filterType: notifyDeviceInstance}
	for index, registration := range []struct {
		name   string
		filter *notificationFilter
	}{{"HID interfaces", &hid}, {"device instances", &instances}} {
		result, _, _ := registerNotification.Call(uintptr(unsafe.Pointer(registration.filter)), sub.key, notificationCallback, uintptr(unsafe.Pointer(&sub.handles[index])))
		if result != 0 {
			return nil, errors.Join(fmt.Errorf("CM_Register_Notification for %s: %w", registration.name, windows.CONFIGRET(result)), sub.Close())
		}
	}
	return sub, nil
}

func (sub *WindowsNotifications) Events() <-chan struct{} { return sub.events }

func (sub *WindowsNotifications) Close() error {
	var errs []error
	for _, handle := range sub.handles {
		if handle != 0 {
			if result, _, _ := unregisterNotification.Call(handle); result != 0 {
				errs = append(errs, windows.CONFIGRET(result))
			}
		}
	}
	notificationChannels.Delete(sub.key)
	return errors.Join(errs...)
}
