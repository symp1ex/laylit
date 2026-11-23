package asustuf

import (
	"context"
	"encoding/binary"
	"fmt"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"laylit/internal/color"
	"laylit/internal/devices"
	"laylit/internal/winio"
)

const (
	deviceID      = "asus-tuf:fx707zc4:internal"
	devicePath    = `\\.\ATKACPI`
	ioctlATK      = 0x22240C
	methodDEVS    = 0x53564544
	rgbMode       = 0x00100056
	applyVolatile = 0xB3
	staticMode    = 0
	normalSpeed   = 0xEB
)

var (
	locateDevNode   = windows.NewLazySystemDLL("cfgmgr32.dll").NewProc("CM_Locate_DevNodeW")
	acpiInstance, _ = windows.UTF16PtrFromString(devices.ATKInstanceID)
	atkName, _      = windows.UTF16PtrFromString("ATKACPI")
	atkPath, _      = windows.UTF16PtrFromString(devicePath)
)

type Provider struct {
	debugf func(string, ...any)
}

func NewProvider(debugf func(string, ...any)) *Provider { return &Provider{debugf: debugf} }

func (*Provider) Name() string { return "ASUS TUF Laptop Keyboard" }

func (provider *Provider) Discover(ctx context.Context) (devices.Inspection, error) {
	inspection := devices.Inspection{Provider: provider.Name()}
	if err := ctx.Err(); err != nil {
		return inspection, err
	}
	matched, err := supportedModel()
	if err != nil || !matched {
		return inspection, err
	}

	var node windows.DEVINST
	result, _, _ := locateDevNode.Call(uintptr(unsafe.Pointer(&node)), uintptr(unsafe.Pointer(acpiInstance)), 0)
	if windows.CONFIGRET(result) == windows.CR_NO_SUCH_DEVNODE {
		return inspection, nil
	}
	if result != 0 {
		return inspection, fmt.Errorf("locate %s: %w", devices.ATKInstanceID, windows.CONFIGRET(result))
	}
	var target [1024]uint16
	if _, err := windows.QueryDosDevice(atkName, &target[0], uint32(len(target))); err != nil {
		return inspection, fmt.Errorf("locate %s for %s: %w", devicePath, deviceID, err)
	}
	inspection.Devices = []devices.Instance{{ID: deviceID, Name: provider.Name(), Location: devicePath, Open: provider.open}}
	return inspection, nil
}

func supportedModel() (bool, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\BIOS`, registry.QUERY_VALUE)
	if err != nil {
		return false, fmt.Errorf("read system model: %w", err)
	}
	defer key.Close()
	manufacturer, _, err := key.GetStringValue("SystemManufacturer")
	if err != nil {
		return false, err
	}
	if !strings.HasPrefix(strings.ToUpper(manufacturer), "ASUSTEK") {
		return false, nil
	}
	model, _, err := key.GetStringValue("SystemProductName")
	if err != nil {
		return false, err
	}
	for _, part := range strings.FieldsFunc(strings.ToUpper(model), func(r rune) bool { return r == ' ' || r == '_' }) {
		if part == "FX707ZC4" {
			return true, nil
		}
	}
	return false, nil
}

func (provider *Provider) open(ctx context.Context) (devices.RGBDevice, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(atkPath, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", devicePath, err)
	}
	return &device{handle: handle, debugf: provider.debugf}, nil
}

type device struct {
	handle windows.Handle
	debugf func(string, ...any)
}

func (device *device) SetColor(ctx context.Context, value color.RGB) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	// ATKACPI expects a method and payload length, then three DEVS arguments.
	// B3 applies the static color without saving it to firmware.
	request := [20]byte{}
	binary.LittleEndian.PutUint32(request[0:4], methodDEVS)
	binary.LittleEndian.PutUint32(request[4:8], 12)
	binary.LittleEndian.PutUint32(request[8:12], rgbMode)
	copy(request[12:], []byte{applyVolatile, staticMode, value.R, value.G, value.B, normalSpeed})
	var response [1024]byte
	n, err := winio.Overlapped(ctx, device.handle, func(n *uint32, overlapped *windows.Overlapped) error {
		return windows.DeviceIoControl(device.handle, ioctlATK, &request[0], uint32(len(request)),
			&response[0], uint32(len(response)), n, overlapped)
	})
	runtime.KeepAlive(&request)
	runtime.KeepAlive(&response)
	if device.debugf != nil {
		device.debugf("ATKACPI device_id=%s method=DEVS rgb=%s response_bytes=%d status=0x%08X error=%v",
			deviceID, value, n, binary.LittleEndian.Uint32(response[:4]), err)
	}
	if err != nil {
		return fmt.Errorf("ATKACPI DeviceIoControl: %w", err)
	}
	if n < 4 || n > uint32(len(response)) {
		return fmt.Errorf("ATKACPI DEVS returned %d bytes, expected at least 4", n)
	}
	if status := binary.LittleEndian.Uint32(response[:4]); status != 1 {
		return fmt.Errorf("ATKACPI DEVS rejected static color: status 0x%08X", status)
	}
	return nil
}

// Static black turns off the LEDs while preserving the brightness setting.
func (device *device) Off(ctx context.Context) error { return device.SetColor(ctx, color.RGB{}) }
func (device *device) Close() error                  { return windows.Close(device.handle) }
