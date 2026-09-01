package keyboard

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const hidpStatusSuccess = 0x00110000

var (
	hidDLL                               = windows.NewLazySystemDLL("hid.dll")
	procHidDGetHidGuid                   = hidDLL.NewProc("HidD_GetHidGuid")
	procHidDGetAttributes                = hidDLL.NewProc("HidD_GetAttributes")
	procHidDGetManufacturerString        = hidDLL.NewProc("HidD_GetManufacturerString")
	procHidDGetProductString             = hidDLL.NewProc("HidD_GetProductString")
	procHidDGetSerialNumberString        = hidDLL.NewProc("HidD_GetSerialNumberString")
	procHidDGetPreparsedData             = hidDLL.NewProc("HidD_GetPreparsedData")
	procHidDFreePreparsedData            = hidDLL.NewProc("HidD_FreePreparsedData")
	procHidPGetCaps                      = hidDLL.NewProc("HidP_GetCaps")
	setupAPIDLL                          = windows.NewLazySystemDLL("setupapi.dll")
	procSetupDiGetClassDevsW             = setupAPIDLL.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInterfaces      = setupAPIDLL.NewProc("SetupDiEnumDeviceInterfaces")
	procSetupDiGetDeviceInterfaceDetailW = setupAPIDLL.NewProc("SetupDiGetDeviceInterfaceDetailW")
	procSetupDiDestroyDeviceInfoList     = setupAPIDLL.NewProc("SetupDiDestroyDeviceInfoList")
	interfacePattern                     = regexp.MustCompile(`(?i)&mi_([0-9a-f]{2})`)
)

type hiddAttributes struct {
	Size          uint32
	VendorID      uint16
	ProductID     uint16
	VersionNumber uint16
}

type hidpCaps struct {
	Usage                     uint16
	UsagePage                 uint16
	InputReportByteLength     uint16
	OutputReportByteLength    uint16
	FeatureReportByteLength   uint16
	Reserved                  [17]uint16
	NumberLinkCollectionNodes uint16
	NumberInputButtonCaps     uint16
	NumberInputValueCaps      uint16
	NumberInputDataIndices    uint16
	NumberOutputButtonCaps    uint16
	NumberOutputValueCaps     uint16
	NumberOutputDataIndices   uint16
	NumberFeatureButtonCaps   uint16
	NumberFeatureValueCaps    uint16
	NumberFeatureDataIndices  uint16
}

type spDeviceInterfaceData struct {
	CbSize             uint32
	InterfaceClassGuid windows.GUID
	Flags              uint32
	Reserved           uintptr
}

type spDeviceInterfaceDetailDataW struct {
	CbSize     uint32
	DevicePath [1]uint16
}

type hidDevice struct {
	handle             windows.Handle
	inputReportLength  int
	outputReportLength int
	closeOnce          sync.Once
	closeErr           error
}

func enumerateHID(wantVendorID, wantProductID uint16) ([]Info, error) {
	guid := new(windows.GUID)
	procHidDGetHidGuid.Call(uintptr(unsafe.Pointer(guid)))

	result, _, callErr := procSetupDiGetClassDevsW.Call(
		uintptr(unsafe.Pointer(guid)),
		0,
		0,
		uintptr(windows.DIGCF_PRESENT|windows.DIGCF_DEVICEINTERFACE),
	)
	deviceSet := windows.Handle(result)
	if deviceSet == windows.InvalidHandle {
		return nil, callErr
	}
	defer procSetupDiDestroyDeviceInfoList.Call(uintptr(deviceSet))

	pathNeedle := fmt.Sprintf("vid_%04x&pid_%04x", wantVendorID, wantProductID)
	var infos []Info
	for index := uint32(0); ; index++ {
		interfaceData := spDeviceInterfaceData{CbSize: uint32(unsafe.Sizeof(spDeviceInterfaceData{}))}
		ok, _, err := procSetupDiEnumDeviceInterfaces.Call(
			uintptr(deviceSet),
			0,
			uintptr(unsafe.Pointer(guid)),
			uintptr(index),
			uintptr(unsafe.Pointer(&interfaceData)),
		)
		if ok == 0 {
			if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
				break
			}
			return nil, err
		}

		path, err := deviceInterfacePath(deviceSet, &interfaceData)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(strings.ToLower(path), pathNeedle) {
			continue
		}

		info, err := readHIDInfo(path)
		if err != nil {
			return nil, fmt.Errorf("read HID metadata for %q: %w", path, err)
		}
		if info.VendorID == wantVendorID && info.ProductID == wantProductID {
			infos = append(infos, info)
		}
	}
	return infos, nil
}

func deviceInterfacePath(deviceSet windows.Handle, interfaceData *spDeviceInterfaceData) (string, error) {
	var requiredSize uint32
	ok, _, err := procSetupDiGetDeviceInterfaceDetailW.Call(
		uintptr(deviceSet),
		uintptr(unsafe.Pointer(interfaceData)),
		0,
		0,
		uintptr(unsafe.Pointer(&requiredSize)),
		0,
	)
	if ok == 0 && !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		return "", err
	}
	if requiredSize == 0 {
		return "", errors.New("SetupDiGetDeviceInterfaceDetail returned an empty path buffer")
	}

	buffer := make([]byte, requiredSize)
	detail := (*spDeviceInterfaceDetailDataW)(unsafe.Pointer(&buffer[0]))
	detail.CbSize = uint32(unsafe.Sizeof(spDeviceInterfaceDetailDataW{}))
	ok, _, err = procSetupDiGetDeviceInterfaceDetailW.Call(
		uintptr(deviceSet),
		uintptr(unsafe.Pointer(interfaceData)),
		uintptr(unsafe.Pointer(detail)),
		uintptr(requiredSize),
		uintptr(unsafe.Pointer(&requiredSize)),
		0,
	)
	if ok == 0 {
		return "", err
	}
	return windows.UTF16PtrToString(&detail.DevicePath[0]), nil
}

func readHIDInfo(path string) (Info, error) {
	handle, err := createHIDFile(path, 0, 0)
	if err != nil {
		return Info{}, err
	}
	defer windows.Close(handle)

	attributes := hiddAttributes{Size: uint32(unsafe.Sizeof(hiddAttributes{}))}
	ok, _, err := procHidDGetAttributes.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&attributes)),
	)
	if ok == 0 {
		return Info{}, err
	}

	caps, err := getHIDCaps(handle)
	if err != nil {
		return Info{}, err
	}
	return Info{
		Path:                path,
		VendorID:            attributes.VendorID,
		ProductID:           attributes.ProductID,
		Release:             attributes.VersionNumber,
		Interface:           interfaceNumberFromPath(path),
		UsagePage:           caps.UsagePage,
		Usage:               caps.Usage,
		Serial:              hidString(handle, procHidDGetSerialNumberString),
		Manufacturer:        hidString(handle, procHidDGetManufacturerString),
		Product:             hidString(handle, procHidDGetProductString),
		InputReportLength:   caps.InputReportByteLength,
		OutputReportLength:  caps.OutputReportByteLength,
		FeatureReportLength: caps.FeatureReportByteLength,
	}, nil
}

func interfaceNumberFromPath(path string) int {
	match := interfacePattern.FindStringSubmatch(path)
	if len(match) != 2 {
		return -1
	}
	value, err := strconv.ParseUint(match[1], 16, 8)
	if err != nil {
		return -1
	}
	return int(value)
}

func hidString(handle windows.Handle, procedure *windows.LazyProc) string {
	buffer := make([]uint16, 128)
	ok, _, _ := procedure.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)*2),
	)
	if ok == 0 {
		return ""
	}
	return windows.UTF16ToString(buffer)
}

func getHIDCaps(handle windows.Handle) (hidpCaps, error) {
	var preparsedData uintptr
	ok, _, err := procHidDGetPreparsedData.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&preparsedData)),
	)
	if ok == 0 {
		return hidpCaps{}, err
	}
	defer procHidDFreePreparsedData.Call(preparsedData)

	var caps hidpCaps
	status, _, err := procHidPGetCaps.Call(preparsedData, uintptr(unsafe.Pointer(&caps)))
	if status != hidpStatusSuccess {
		if err == nil || errors.Is(err, windows.NOERROR) {
			err = fmt.Errorf("HidP_GetCaps status 0x%X", status)
		}
		return hidpCaps{}, err
	}
	return caps, nil
}

func openHID(path string) (*hidDevice, error) {
	handle, err := createHIDFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_FLAG_OVERLAPPED)
	if err != nil {
		return nil, err
	}
	caps, err := getHIDCaps(handle)
	if err != nil {
		windows.Close(handle)
		return nil, err
	}
	return &hidDevice{
		handle:             handle,
		inputReportLength:  int(caps.InputReportByteLength),
		outputReportLength: int(caps.OutputReportByteLength),
	}, nil
}

func createHIDFile(path string, access uint32, flags uint32) (windows.Handle, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(
		pathPointer,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		flags,
		0,
	)
}

func (device *hidDevice) Write(ctx context.Context, report []byte) (int, error) {
	if len(report) != device.outputReportLength {
		return 0, fmt.Errorf("output report is %d bytes; HID descriptor requires %d", len(report), device.outputReportLength)
	}
	return device.overlappedIO(ctx, report, windows.WriteFile)
}

func (device *hidDevice) Read(ctx context.Context, report []byte) (int, error) {
	if len(report) != device.inputReportLength {
		return 0, fmt.Errorf("input buffer is %d bytes; HID descriptor requires %d", len(report), device.inputReportLength)
	}
	return device.overlappedIO(ctx, report, windows.ReadFile)
}

type overlappedOperation func(windows.Handle, []byte, *uint32, *windows.Overlapped) error

func (device *hidDevice) overlappedIO(ctx context.Context, buffer []byte, operation overlappedOperation) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.Close(event)

	overlapped := &windows.Overlapped{HEvent: event}
	var transferred uint32
	err = operation(device.handle, buffer, &transferred, overlapped)
	if err == nil {
		return int(transferred), nil
	}
	if !errors.Is(err, windows.ERROR_IO_PENDING) {
		return 0, err
	}

	timeout, err := contextTimeout(ctx)
	if err != nil {
		_ = windows.CancelIoEx(device.handle, overlapped)
		return 0, err
	}
	status, err := windows.WaitForSingleObject(event, timeout)
	if err != nil {
		_ = windows.CancelIoEx(device.handle, overlapped)
		return 0, err
	}
	if status == uint32(windows.WAIT_TIMEOUT) {
		_ = windows.CancelIoEx(device.handle, overlapped)
		_ = windows.GetOverlappedResult(device.handle, overlapped, &transferred, true)
		return 0, context.DeadlineExceeded
	}
	if status != windows.WAIT_OBJECT_0 {
		_ = windows.CancelIoEx(device.handle, overlapped)
		return 0, fmt.Errorf("unexpected overlapped I/O wait status: %d", status)
	}
	if err := windows.GetOverlappedResult(device.handle, overlapped, &transferred, false); err != nil {
		return 0, err
	}
	return int(transferred), nil
}

func contextTimeout(ctx context.Context) (uint32, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return windows.INFINITE, nil
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0, context.DeadlineExceeded
	}
	milliseconds := (remaining + time.Millisecond - 1) / time.Millisecond
	if milliseconds >= time.Duration(windows.INFINITE) {
		return windows.INFINITE - 1, nil
	}
	return uint32(milliseconds), nil
}

func (device *hidDevice) Close() error {
	device.closeOnce.Do(func() {
		if err := windows.CancelIoEx(device.handle, nil); err != nil && !errors.Is(err, windows.ERROR_NOT_FOUND) {
			device.closeErr = err
		}
		if err := windows.Close(device.handle); err != nil && device.closeErr == nil {
			device.closeErr = err
		}
	})
	return device.closeErr
}
