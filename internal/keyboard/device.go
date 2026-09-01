package keyboard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	vendorID     = 0x320F
	productID    = 0x5000
	rgbInterface = 1
	rgbUsagePage = 0xFF1C
)

type Options struct {
	Debug       bool
	DebugWriter io.Writer
}

type Info struct {
	Path                string
	VendorID            uint16
	ProductID           uint16
	Release             uint16
	Interface           int
	UsagePage           uint16
	Usage               uint16
	Serial              string
	Manufacturer        string
	Product             string
	InputReportLength   uint16
	OutputReportLength  uint16
	FeatureReportLength uint16
}

func (info Info) RGBCandidate() bool {
	return info.Interface == rgbInterface && info.UsagePage == rgbUsagePage
}

type Device struct {
	hid     *hidDevice
	info    Info
	options Options
}

func Enumerate(options Options) ([]Info, error) {
	infos, err := enumerateHID(vendorID, productID)
	if err != nil {
		return nil, fmt.Errorf("enumerate HID interfaces: %w", err)
	}
	for _, info := range infos {
		debugf(options, "found VID=%04X PID=%04X path=%s interface=%s usagePage=%04X usage=%04X candidate=%s",
			info.VendorID, info.ProductID, info.Path, formatInterface(info.Interface),
			info.UsagePage, info.Usage, yesNo(info.RGBCandidate()))
	}
	return infos, nil
}

// Open selects exactly the same class of HID collection as OpenRGB's detector
// for 320F:5000: USB interface 1 and vendor Usage Page 0xFF1C. OpenRGB does not
// constrain the Usage ID, so this implementation deliberately does not invent
// one. It refuses ambiguity instead of opening the first matching collection.
func Open(options Options) (*Device, error) {
	infos, err := Enumerate(options)
	if err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		return nil, errors.New("EVision keyboard 320F:5000 not found")
	}

	var candidates []Info
	for _, info := range infos {
		if info.RGBCandidate() {
			candidates = append(candidates, info)
		}
	}
	if len(candidates) == 0 {
		return nil, errors.New("RGB HID interface not found: expected interface 1 with usage page FF1C")
	}
	if len(candidates) != 1 {
		return nil, fmt.Errorf("ambiguous RGB HID interface: found %d interface 1 collections with usage page FF1C", len(candidates))
	}

	selected := candidates[0]
	debugf(options, "selected interface=%s usagePage=%04X usage=%04X path=%s",
		formatInterface(selected.Interface), selected.UsagePage, selected.Usage, selected.Path)

	hid, err := openHID(selected.Path)
	if err != nil {
		return nil, fmt.Errorf("open RGB HID interface: %w", err)
	}
	if hid.outputReportLength != reportSize {
		_ = hid.Close()
		return nil, fmt.Errorf("unsupported output report size: device reports %d bytes, protocol requires %d", hid.outputReportLength, reportSize)
	}
	if hid.inputReportLength != reportSize {
		_ = hid.Close()
		return nil, fmt.Errorf("unsupported input report size: device reports %d bytes, protocol requires %d", hid.inputReportLength, reportSize)
	}

	return &Device{hid: hid, info: selected, options: options}, nil
}

func (device *Device) Close() error {
	if device == nil || device.hid == nil {
		return nil
	}
	return device.hid.Close()
}

func (device *Device) SetColor(red, green, blue byte) error {
	report := buildStaticReport(red, green, blue, brightnessHighest)
	return device.sendReport(report[:])
}

func (device *Device) Off() error {
	// OpenRGB defines EVISION_KB_BRIGHTNESS_LOWEST (0x00) as "off". Keep the
	// confirmed static-mode command and send black with that brightness.
	report := buildStaticReport(0, 0, 0, brightnessOff)
	return device.sendReport(report[:])
}

func (device *Device) sendReport(report []byte) error {
	if len(report) != reportSize {
		return fmt.Errorf("invalid output report size: got %d, want %d", len(report), reportSize)
	}
	debugf(device.options, "output report (%d bytes): %s", len(report), formatHex(report))

	writeContext, cancelWrite := context.WithTimeout(context.Background(), 2*time.Second)
	written, err := device.hid.Write(writeContext, report)
	cancelWrite()
	if err != nil {
		return fmt.Errorf("write output report: %w", err)
	}
	if written != reportSize {
		return fmt.Errorf("write output report: wrote %d bytes, want %d", written, reportSize)
	}
	debugf(device.options, "write: %d bytes", written)

	// OpenRGB performs one hid_read after every hid_write for this controller.
	// The response is an acknowledgement; OpenRGB does not decode its contents.
	readContext, cancelRead := context.WithTimeout(context.Background(), time.Second)
	response := make([]byte, reportSize)
	read, err := device.hid.Read(readContext, response)
	cancelRead()
	if err != nil {
		return fmt.Errorf("read device acknowledgement: %w", err)
	}
	if read != reportSize {
		return fmt.Errorf("read device acknowledgement: got %d bytes, want %d", read, reportSize)
	}
	debugf(device.options, "read acknowledgement: %d bytes", read)
	return nil
}

func debugf(options Options, format string, args ...any) {
	if !options.Debug {
		return
	}
	writer := options.DebugWriter
	if writer == nil {
		writer = io.Discard
	}
	fmt.Fprintf(writer, "DEBUG "+format+"\n", args...)
}

func formatHex(data []byte) string {
	parts := make([]string, len(data))
	for i, value := range data {
		parts[i] = fmt.Sprintf("%02X", value)
	}
	return strings.Join(parts, " ")
}

func formatInterface(value int) string {
	if value < 0 {
		return "N/A"
	}
	return fmt.Sprintf("%d", value)
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
