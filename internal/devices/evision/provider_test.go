package evision

import (
	"context"
	"strings"
	"testing"

	"laylit/internal/hid"
)

func TestRGBCandidateDoesNotConstrainUsageID(t *testing.T) {
	for _, usage := range []uint16{0, 1, 0xFFFF} {
		if !rgbCandidate(hid.Info{Interface: 1, UsagePage: 0xFF1C, Usage: usage}) {
			t.Fatalf("usage %04X was unexpectedly rejected", usage)
		}
	}
	if rgbCandidate(hid.Info{Interface: 0, UsagePage: 0xFF1C}) {
		t.Fatal("wrong interface accepted")
	}
	if rgbCandidate(hid.Info{Interface: 1, UsagePage: 1}) {
		t.Fatal("wrong usage page accepted")
	}
}

func TestSelectCandidateRefusesZeroAndAmbiguous(t *testing.T) {
	if _, err := selectCandidate(nil); err == nil {
		t.Fatal("zero candidates accepted")
	}
	candidate := hid.Info{Interface: 1, UsagePage: 0xFF1C}
	if _, err := selectCandidate([]hid.Info{candidate, candidate}); err == nil {
		t.Fatal("ambiguous candidates accepted")
	}
}

func rgbCollection(path, parent, serial, container string) hid.Info {
	return hid.Info{VendorID: vendorID, ProductID: productID, Path: path, Interface: rgbInterface, UsagePage: rgbUsagePage,
		USBInstanceID: parent, USBSerial: serial, ContainerID: container, InputReportLength: reportSize, OutputReportLength: reportSize}
}

func TestDiscoveryDistinguishesIdenticalKeyboards(t *testing.T) {
	for _, test := range []struct{ name, firstSerial, secondSerial, firstContainer, secondContainer, wantFirst, wantSecond string }{
		{"unique serials", "one", "two", "a", "b", "serial:one:a", "serial:two:b"},
		{"no serials", "", "", "a", "b", "container:a", "container:b"},
		{"duplicate serials", "same", "same", "a", "b", "serial:same:a", "serial:same:b"},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &fakeTransport{
				infos: []hid.Info{
					rgbCollection("first-rgb", "first-parent", test.firstSerial, test.firstContainer),
					rgbCollection("second-rgb", "second-parent", test.secondSerial, test.secondContainer),
				},
				connection: &fakeConnection{inputLength: reportSize, outputLength: reportSize},
			}
			provider := NewProvider(transport, Options{})
			found, err := provider.Discover(context.Background())
			if err != nil || len(found.Devices) != 2 {
				t.Fatalf("devices = %v, error = %v", found.Devices, err)
			}
			if found.Devices[0].ID != "usb:320f:5000:"+test.wantFirst || found.Devices[1].ID != "usb:320f:5000:"+test.wantSecond {
				t.Fatalf("unexpected identities: %v", found.Devices)
			}
			if transport.openedPath != "" {
				t.Fatal("discovery opened a device for writes")
			}
			device, err := found.Devices[1].Open(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer device.Close()
			if transport.openedPath != "second-rgb" {
				t.Fatalf("opened %q instead of selected RGB collection", transport.openedPath)
			}
			transport.infos = transport.infos[1:]
			remaining, err := provider.Discover(context.Background())
			if err != nil || len(remaining.Devices) != 1 || remaining.Devices[0].ID != found.Devices[1].ID {
				t.Fatalf("remaining device changed ID: %v, error = %v", remaining.Devices, err)
			}
		})
	}
}

func TestDiscoveryRejectsOnlyAmbiguousPhysicalKeyboard(t *testing.T) {
	transport := &fakeTransport{infos: []hid.Info{
		rgbCollection("ambiguous-rgb-a", "first-parent", "", "a"),
		rgbCollection("ambiguous-rgb-b", "first-parent", "", "a"),
		rgbCollection("other-rgb", "second-parent", "", "b"),
	}}
	for _, usage := range []uint16{0, 1, 0xFFFF} {
		transport.infos[2].Usage = usage
		found, err := NewProvider(transport, Options{}).Discover(context.Background())
		if err == nil || !strings.Contains(err.Error(), "ambiguous RGB HID interface") ||
			len(found.Devices) != 1 || found.Devices[0].Location != "other-rgb" {
			t.Fatalf("devices = %v, error = %v", found.Devices, err)
		}
	}
}

func TestDiscoveryRefusesCollidingContainerIDs(t *testing.T) {
	transport := &fakeTransport{infos: []hid.Info{
		rgbCollection("first-rgb", "first-parent", "same", "same-container"),
		rgbCollection("second-rgb", "second-parent", "same", "same-container"),
	}}
	found, err := NewProvider(transport, Options{}).Discover(context.Background())
	if err == nil || len(found.Devices) != 0 {
		t.Fatalf("colliding identities were accepted: %v, error = %v", found.Devices, err)
	}
}

func TestDiscoveryRejectsWrongReportSize(t *testing.T) {
	info := rgbCollection("candidate", "parent", "", "container")
	info.InputReportLength = 63
	found, err := NewProvider(&fakeTransport{infos: []hid.Info{info}}, Options{}).Discover(context.Background())
	if err == nil || !strings.Contains(err.Error(), "report size") || len(found.Devices) != 0 {
		t.Fatalf("invalid collection discovered: %v, error = %v", found.Devices, err)
	}
}
