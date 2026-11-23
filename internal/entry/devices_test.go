package entry

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"laylit/internal/devices"
	"laylit/internal/devices/evision"
	"laylit/internal/hid"
)

func TestCLISingleEVisionAndDeviceSelection(t *testing.T) {
	var out, diagnostics bytes.Buffer
	transport := &cliHID{infos: []hid.Info{cliEVision("first", "a")}}
	provider := evision.NewProvider(transport, evision.Options{Debug: true, DebugWriter: &diagnostics})
	registry := devices.NewRegistry(provider)
	ctx := context.Background()
	if err := printInfo(ctx, &out, registry); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Device ID: usb:320f:5000:container:a", "Usage Page: FF1C", "RGB candidate: yes"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("info missing %q: %s", want, &out)
		}
	}
	if err := runDeviceCommand(ctx, &out, &diagnostics, registry, true, "", []string{"set", "123456"}); err != nil {
		t.Fatal(err)
	}
	if transport.opened != "first" || !bytes.Equal(transport.report[13:16], []byte{0x12, 0x34, 0x56}) {
		t.Fatalf("set opened %q and wrote % X", transport.opened, transport.report)
	}
	if err := runDeviceCommand(ctx, &out, &diagnostics, registry, true, "", []string{"off"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(transport.report[9:16], make([]byte, 7)) || transport.closes != 2 {
		t.Fatalf("off report = % X, closes = %d", transport.report, transport.closes)
	}
	if !strings.Contains(diagnostics.String(), "DEBUG output report (64 bytes)") ||
		!strings.Contains(diagnostics.String(), "DEBUG selected device_id=usb:320f:5000:container:a operation=set") {
		t.Fatalf("missing debug output: %s", &diagnostics)
	}

	transport.infos = append(transport.infos, cliEVision("second", "b"))
	for _, args := range [][]string{{"set", "123456"}, {"off"}, {"off", "--device", "missing"}} {
		transport.opened = ""
		if err := runDeviceCommand(ctx, io.Discard, io.Discard, registry, false, "", args); err == nil || transport.opened != "" {
			t.Fatalf("%v selected %q, error = %v", args, transport.opened, err)
		} else if len(args) < 3 && !strings.Contains(err.Error(), "--device <id>") {
			t.Fatalf("%v did not explain how to select a device: %v", args, err)
		}
	}
	const secondID = "usb:320f:5000:container:b"
	for _, args := range [][]string{{"set", "--device", secondID, "123456"}, {"off", "--device=" + secondID}} {
		if err := runDeviceCommand(ctx, io.Discard, io.Discard, registry, false, "", args); err != nil || transport.opened != "second" {
			t.Fatalf("%v selected %q, error = %v", args, transport.opened, err)
		}
	}
}

func cliEVision(path, container string) hid.Info {
	return hid.Info{Path: path, VendorID: 0x320F, ProductID: 0x5000, Interface: 1, UsagePage: 0xFF1C,
		USBInstanceID: path, ContainerID: container, InputReportLength: 64, OutputReportLength: 64}
}

type cliHID struct {
	infos  []hid.Info
	opened string
	report []byte
	closes int
}

func (device *cliHID) Enumerate(context.Context, uint16, uint16) ([]hid.Info, error) {
	return device.infos, nil
}
func (device *cliHID) Open(_ context.Context, path string) (hid.Connection, error) {
	device.opened = path
	return device, nil
}
func (*cliHID) InputReportLength() int  { return 64 }
func (*cliHID) OutputReportLength() int { return 64 }
func (device *cliHID) Write(_ context.Context, report []byte) (int, error) {
	device.report = append([]byte(nil), report...)
	return len(report), nil
}
func (*cliHID) Read(_ context.Context, report []byte) (int, error) { return len(report), nil }
func (device *cliHID) Close() error                                { device.closes++; return nil }
