package hid

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	cfgMgr            = windows.NewLazySystemDLL("cfgmgr32.dll")
	getParent         = cfgMgr.NewProc("CM_Get_Parent")
	getDeviceID       = cfgMgr.NewProc("CM_Get_Device_IDW")
	getDeviceProperty = cfgMgr.NewProc("CM_Get_DevNode_PropertyW")
	containerIDKey    = windows.DEVPROPKEY{FmtID: windows.DEVPROPGUID{
		Data1: 0x8C7ED206, Data2: 0x3F8A, Data3: 0x4827,
		Data4: [8]byte{0xB3, 0xAB, 0xAE, 0x9E, 0x1F, 0xAE, 0xFC, 0x6C},
	}, PID: 2}
	capabilitiesKey = windows.DEVPROPKEY{FmtID: windows.DEVPROPGUID{
		Data1: 0xA45C254E, Data2: 0xDF1C, Data3: 0x4EFD,
		Data4: [8]byte{0x80, 0x20, 0x67, 0xD1, 0x46, 0xA8, 0x50, 0xE0},
	}, PID: 17}
)

func readUSBIdentity(info *Info, devInst uint32, vidPID string) error {
	// A composite USB device can have several interface devnodes and HID
	// collections. Walk up to the USB device itself before grouping them.
	for {
		var idBuffer [200]uint16
		status, _, _ := getDeviceID.Call(uintptr(devInst), uintptr(unsafe.Pointer(&idBuffer[0])), uintptr(len(idBuffer)), 0)
		if status != 0 {
			return fmt.Errorf("CM_Get_Device_ID: CONFIGRET 0x%X", status)
		}
		id := strings.ToLower(windows.UTF16ToString(idBuffer[:]))
		if strings.HasPrefix(id, "usb\\"+vidPID+"\\") {
			info.USBInstanceID = id
			break
		}
		depthStatus, _, _ := getParent.Call(uintptr(unsafe.Pointer(&devInst)), uintptr(devInst), 0)
		if depthStatus != 0 {
			return fmt.Errorf("find physical USB parent: CONFIGRET 0x%X", depthStatus)
		}
	}
	var capabilities uint32
	if err := readDeviceProperty(devInst, &capabilitiesKey, windows.DEVPROP_TYPE_INT32, unsafe.Pointer(&capabilities), 4); err != nil {
		return err
	}
	const uniqueID = 0x10 // CM_DEVCAP_UNIQUEID
	if capabilities&uniqueID != 0 {
		info.USBSerial = info.USBInstanceID[strings.LastIndexByte(info.USBInstanceID, '\\')+1:]
	}
	var container windows.GUID
	if err := readDeviceProperty(devInst, &containerIDKey, windows.DEVPROP_TYPE_GUID, unsafe.Pointer(&container), 16); err != nil {
		if info.USBSerial != "" && errors.Is(err, windows.CR_NO_SUCH_VALUE) {
			return nil
		}
		return err
	}
	if container != (windows.GUID{}) {
		info.ContainerID = strings.ToLower(strings.Trim(container.String(), "{}"))
	}
	return nil
}

func readDeviceProperty(devInst uint32, key *windows.DEVPROPKEY, wantType windows.DEVPROPTYPE, target unsafe.Pointer, size uint32) error {
	var propertyType windows.DEVPROPTYPE
	wantSize := size
	status, _, _ := getDeviceProperty.Call(uintptr(devInst), uintptr(unsafe.Pointer(key)),
		uintptr(unsafe.Pointer(&propertyType)), uintptr(target), uintptr(unsafe.Pointer(&size)), 0)
	if status != 0 {
		return fmt.Errorf("CM_Get_DevNode_Property (%d): %w", key.PID, windows.CONFIGRET(status))
	}
	// Windows reports capabilities as UINT32 on some USB devices.
	validType := propertyType == wantType ||
		(key == &capabilitiesKey && propertyType == windows.DEVPROP_TYPE_UINT32)
	if !validType || size != wantSize {
		return fmt.Errorf("unexpected device property %d type 0x%X or size %d", key.PID, propertyType, size)
	}
	return nil
}
