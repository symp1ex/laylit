package evision

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"

	"laylit/internal/devices"
	"laylit/internal/hid"
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

func (provider *Provider) Discover(ctx context.Context) (devices.Inspection, error) {
	inspection := devices.Inspection{
		Provider: provider.Name(), Description: "HID interfaces for 320F:5000",
		NotFoundMessage: "EVision keyboard 320F:5000 not found",
	}
	infos, err := provider.transport.Enumerate(ctx, vendorID, productID)
	if err != nil {
		return inspection, fmt.Errorf("enumerate HID interfaces: %w", err)
	}
	groups := make(map[string][]hid.Info)
	var errs []error
	for _, info := range infos {
		provider.debugf("found VID=%04X PID=%04X path=%s interface=%s usagePage=%04X usage=%04X candidate=%s",
			info.VendorID, info.ProductID, info.Path, formatInterface(info.Interface),
			info.UsagePage, info.Usage, yesNo(rgbCandidate(info)))
		inspection.Collections = append(inspection.Collections, collectionInfo(info))
		if info.USBInstanceID == "" {
			if info.Interface == rgbInterface {
				// An unidentified collection may belong to any of the identified
				// keyboards. Its ambiguity cannot be scoped safely.
				return inspection, errors.Join(fmt.Errorf("cannot identify RGB HID collection %q", info.Path), info.DiscoveryError)
			}
			continue
		}
		groups[info.USBInstanceID] = append(groups[info.USBInstanceID], info)
	}
	var keys []string
	serialCounts := make(map[string]int)
	containerCounts := make(map[string]int)
	for key, group := range groups {
		keys = append(keys, key)
		serialCounts[group[0].USBSerial]++
		containerCounts[group[0].ContainerID]++
	}
	sort.Strings(keys)
	for _, key := range keys {
		group := groups[key]
		id, err := usbDeviceID(group[0], serialCounts, containerCounts)
		if err != nil {
			errs = append(errs, fmt.Errorf("identify EVision keyboard %q: %w", key, err))
			continue
		}
		selected, err := selectCandidate(group)
		if err != nil {
			errs = append(errs, fmt.Errorf("device %s: %w", id, err))
			continue
		}
		name := selected.Product
		if name == "" {
			name = provider.Name()
		}
		inspection.Devices = append(inspection.Devices, devices.Instance{
			ID: id, Name: name, Location: selected.Path, Open: func(ctx context.Context) (devices.RGBDevice, error) {
				return provider.open(ctx, id, selected)
			},
		})
	}
	return inspection, errors.Join(errs...)
}

func usbDeviceID(info hid.Info, serialCounts, containerCounts map[string]int) (string, error) {
	prefix := fmt.Sprintf("usb:%04x:%04x:", info.VendorID, info.ProductID)
	if info.USBSerial != "" {
		id := prefix + "serial:" + url.PathEscape(info.USBSerial)
		if info.ContainerID != "" {
			// Keep the container in the ID even when the serial is unique
			// in the current scan. Discovering a second keyboard with the
			// same serial must not reset either keyboard's settings.
			id += ":" + info.ContainerID
		}
		if serialCounts[info.USBSerial] == 1 || (info.ContainerID != "" && containerCounts[info.ContainerID] == 1) {
			return id, nil
		}
		return "", errors.New("duplicate USB serial without a unique Windows ContainerId")
	}
	if info.ContainerID == "" || containerCounts[info.ContainerID] != 1 {
		return "", errors.New("no unique USB serial or Windows ContainerId")
	}
	return prefix + "container:" + info.ContainerID, nil
}

func (provider *Provider) open(ctx context.Context, id string, selected hid.Info) (devices.RGBDevice, error) {
	provider.debugf("selected device_id=%s interface=%s usagePage=%04X usage=%04X path=%s", id,
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

// selectCandidate preserves the v1/OpenRGB selector exactly: interface 1 and
// Usage Page FF1C, with deliberately no Usage ID constraint.
func selectCandidate(infos []hid.Info) (hid.Info, error) {
	var candidates []hid.Info
	for _, info := range infos {
		if info.Interface == rgbInterface && info.DiscoveryError != nil {
			return hid.Info{}, info.DiscoveryError
		}
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
	selected := candidates[0]
	if selected.InputReportLength != reportSize || selected.OutputReportLength != reportSize {
		return hid.Info{}, fmt.Errorf("unsupported RGB HID report sizes: input=%d output=%d, protocol requires %d",
			selected.InputReportLength, selected.OutputReportLength, reportSize)
	}
	return selected, nil
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
