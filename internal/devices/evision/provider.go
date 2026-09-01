package evision

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"evision-rgb/internal/devices"
	"evision-rgb/internal/hid"
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

type Provider struct {
	transport hid.Transport
	options   Options
}

func NewProvider(transport hid.Transport, options Options) *Provider {
	return &Provider{transport: transport, options: options}
}

func (provider *Provider) Name() string { return "EVision 320F:5000" }

func (provider *Provider) Inspect(ctx context.Context) (devices.Inspection, error) {
	infos, err := provider.enumerate(ctx)
	if err != nil {
		return devices.Inspection{}, err
	}
	collections := make([]devices.CollectionInfo, 0, len(infos))
	for _, info := range infos {
		collections = append(collections, collectionInfo(info))
	}
	return devices.Inspection{
		Provider: provider.Name(), Description: "HID interfaces for 320F:5000",
		NotFoundMessage: "EVision keyboard 320F:5000 not found", Collections: collections,
	}, nil
}

func (provider *Provider) Open(ctx context.Context) (devices.RGBDevice, error) {
	infos, err := provider.enumerate(ctx)
	if err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		return nil, fmt.Errorf("%w: EVision keyboard 320F:5000 not found", devices.ErrNotFound)
	}

	selected, err := selectCandidate(infos)
	if err != nil {
		return nil, err
	}
	provider.debugf("selected interface=%s usagePage=%04X usage=%04X path=%s",
		formatInterface(selected.Interface), selected.UsagePage, selected.Usage, selected.Path)

	connection, err := provider.transport.Open(ctx, selected.Path)
	if err != nil {
		return nil, fmt.Errorf("open RGB HID interface: %w", err)
	}
	if connection.OutputReportLength() != reportSize {
		_ = connection.Close()
		return nil, fmt.Errorf("unsupported output report size: device reports %d bytes, protocol requires %d", connection.OutputReportLength(), reportSize)
	}
	if connection.InputReportLength() != reportSize {
		_ = connection.Close()
		return nil, fmt.Errorf("unsupported input report size: device reports %d bytes, protocol requires %d", connection.InputReportLength(), reportSize)
	}
	return &Device{connection: connection, debugf: provider.debugf}, nil
}

func (provider *Provider) enumerate(ctx context.Context) ([]hid.Info, error) {
	infos, err := provider.transport.Enumerate(ctx, vendorID, productID)
	if err != nil {
		return nil, fmt.Errorf("enumerate HID interfaces: %w", err)
	}
	for _, info := range infos {
		provider.debugf("found VID=%04X PID=%04X path=%s interface=%s usagePage=%04X usage=%04X candidate=%s",
			info.VendorID, info.ProductID, info.Path, formatInterface(info.Interface),
			info.UsagePage, info.Usage, yesNo(rgbCandidate(info)))
	}
	return infos, nil
}

// selectCandidate preserves the v1/OpenRGB selector exactly: interface 1 and
// Usage Page FF1C, with deliberately no Usage ID constraint.
func selectCandidate(infos []hid.Info) (hid.Info, error) {
	var candidates []hid.Info
	for _, info := range infos {
		if rgbCandidate(info) {
			candidates = append(candidates, info)
		}
	}
	if len(candidates) == 0 {
		return hid.Info{}, errors.New("RGB HID interface not found: expected interface 1 with usage page FF1C")
	}
	if len(candidates) != 1 {
		return hid.Info{}, fmt.Errorf("ambiguous RGB HID interface: found %d interface 1 collections with usage page FF1C", len(candidates))
	}
	return candidates[0], nil
}

func rgbCandidate(info hid.Info) bool {
	return info.Interface == rgbInterface && info.UsagePage == rgbUsagePage
}

func collectionInfo(info hid.Info) devices.CollectionInfo {
	return devices.CollectionInfo{
		Path: info.Path, VendorID: info.VendorID, ProductID: info.ProductID,
		Interface: info.Interface, UsagePage: info.UsagePage, Usage: info.Usage,
		Serial: info.Serial, Manufacturer: info.Manufacturer, Product: info.Product,
		InputReportLength: info.InputReportLength, OutputReportLength: info.OutputReportLength,
		FeatureReportLength: info.FeatureReportLength, Candidate: rgbCandidate(info),
	}
}

func (provider *Provider) debugf(format string, args ...any) {
	if !provider.options.Debug {
		return
	}
	writer := provider.options.DebugWriter
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
