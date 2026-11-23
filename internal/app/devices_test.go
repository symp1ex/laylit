package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"laylit/internal/color"
	"laylit/internal/config"
	"laylit/internal/devices"
	"laylit/internal/layouts"
)

func TestRuntimeDeviceWritesAreIndependent(t *testing.T) {
	for _, writeErr := range []error{errors.New("write failed"), context.DeadlineExceeded} {
		t.Run(writeErr.Error(), func(t *testing.T) {
			source := newFakeSource("en")
			slow := &blockedDevice{fakeDevice: newFakeDevice(), started: make(chan struct{}), release: make(chan struct{}), err: writeErr}
			reconnected := newFakeDevice()
			fast := newFakeDevice()
			firstOpen := true
			bad := devices.Instance{ID: "slow", Open: func(context.Context) (devices.RGBDevice, error) {
				if firstOpen {
					firstOpen = false
					return slow, nil
				}
				return reconnected, nil
			}}
			discovery := &fakeDiscovery{devices: []devices.Instance{bad, fakeInstance("fast", fast)}}
			reports := make(chan error, 2)
			startRuntime(t, &Runtime{Layouts: source, Config: configuredRepository(), Devices: discovery, ReportError: func(err error) { reports <- err }})
			await(t, slow.started)
			assertColor(t, fast.applied, color.RGB{R: 1, G: 2, B: 3})
			for _, layout := range []struct {
				id  string
				rgb color.RGB
			}{
				{"ru", color.RGB{R: 4, G: 5, B: 6}},
				{"en", color.RGB{R: 1, G: 2, B: 3}},
				{"ru", color.RGB{R: 4, G: 5, B: 6}},
			} {
				source.subscription.events <- layouts.Layout{ID: layout.id}
				assertColor(t, fast.applied, layout.rgb)
			}
			close(slow.release)
			select {
			case err := <-reports:
				if !errors.Is(err, writeErr) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("failed write was not reported")
			}
			select {
			case got := <-reconnected.applied:
				if want := (color.RGB{R: 4, G: 5, B: 6}); got != want {
					t.Fatalf("retried color = %v, want %v", got, want)
				}
			case <-time.After(2 * deviceRetryDelay):
				t.Fatal("failed keyboard did not reconnect")
			}
			assertNoColor(t, reconnected.applied)
			if !slow.isClosed() || fast.isClosed() {
				t.Fatal("write failure did not close only the failed connection")
			}
		})
	}
}

func TestRuntimeHotplugFromZeroAndBack(t *testing.T) {
	const asusID = "asus-tuf:fx707zc4:internal"
	const usbID = "usb:320f:5000:container:test"
	source := newFakeSource("en")
	repository := configuredRepository()
	repository.value.Devices[asusID] = config.DeviceSettings{Enabled: true, Colors: map[string]string{"en": "#112233", "ru": "#445566"}}
	repository.value.Devices["disabled"] = config.DeviceSettings{Enabled: false, Colors: map[string]string{"ru": "#987654"}}
	repository.value.Devices["absent"] = config.DeviceSettings{Enabled: true}
	discovery := &fakeDiscovery{}
	notifications := &fakeDeviceSubscription{events: make(chan struct{}, 1)}
	ready := make(chan struct{})
	backToEnglish := make(chan struct{}, 1)
	runtime := &Runtime{
		Layouts: source, Config: repository, Devices: discovery,
		WatchDevices: func() (DeviceSubscription, error) { return notifications, nil },
		Ready:        func() error { close(ready); return nil },
		Tracef: func(format string, args ...any) {
			if format == "runtime event received layout_id=%s applied_layout_id=%s" && args[0] == "en" {
				backToEnglish <- struct{}{}
			}
		},
	}
	stop := startRuntime(t, runtime)
	await(t, ready)

	asus, usb, disabled := newFakeDevice(), newFakeDevice(), newFakeDevice()
	asusInstance, disabledInstance := fakeInstance(asusID, asus), fakeInstance("disabled", disabled)
	discovery.connect(notifications, asusInstance, fakeInstance(usbID, usb), disabledInstance)
	assertColor(t, asus.applied, color.RGB{R: 0x11, G: 0x22, B: 0x33})
	assertColor(t, usb.applied, color.RGB{R: 1, G: 2, B: 3})
	assertNoColor(t, disabled.applied)

	discovery.connect(notifications, asusInstance)
	awaitClosed(t, usb)
	source.subscription.events <- layouts.Layout{ID: "ru"}
	assertColor(t, asus.applied, color.RGB{R: 0x44, G: 0x55, B: 0x66})
	usbAgain := newFakeDevice()
	discovery.connect(notifications, asusInstance, fakeInstance(usbID, usbAgain), disabledInstance)
	assertColor(t, usbAgain.applied, color.RGB{R: 4, G: 5, B: 6})
	assertNoColor(t, disabled.applied)
	assertNoColor(t, asus.applied)

	discovery.connect(notifications)
	awaitClosed(t, asus)
	awaitClosed(t, usbAgain)
	source.subscription.events <- layouts.Layout{ID: "en"}
	await(t, backToEnglish)
	usbThird := newFakeDevice()
	discovery.connect(notifications, fakeInstance(usbID, usbThird))
	assertColor(t, usbThird.applied, color.RGB{R: 1, G: 2, B: 3})
	stop()
	if !notifications.closed || !usbThird.isClosed() || !source.subscription.isClosed() {
		t.Fatal("runtime shutdown did not close subscriptions and the remaining connection")
	}
	settings := repository.value.Devices
	if settings["disabled"].Enabled || settings["disabled"].Colors["ru"] != "#987654" ||
		!settings["absent"].Enabled || !settings[usbID].Enabled {
		t.Fatalf("unexpected saved device settings: %#v", settings)
	}
}

func TestRuntimeDiscoversKeyboardsWhenWindowsNotificationsFail(t *testing.T) {
	discovery := &fakeDiscovery{}
	ready := make(chan struct{})
	warnings := make(chan error, 1)
	watchErr := errors.New("CM_Register_Notification failed")
	startRuntime(t, &Runtime{
		Layouts: newFakeSource("en"), Config: configuredRepository(), Devices: discovery,
		WatchDevices: func() (DeviceSubscription, error) { return nil, watchErr },
		ReportError:  func(err error) { warnings <- err },
		Ready:        func() error { close(ready); return nil },
	})
	await(t, ready)
	select {
	case err := <-warnings:
		if !errors.Is(err, watchErr) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("notification failure was not reported")
	}

	device := newFakeDevice()
	discovery.mu.Lock()
	discovery.devices = []devices.Instance{fakeInstance("keyboard", device)}
	discovery.mu.Unlock()
	select {
	case got := <-device.applied:
		if want := (color.RGB{R: 1, G: 2, B: 3}); got != want {
			t.Fatalf("applied color = %v, want %v", got, want)
		}
	case <-time.After(2 * deviceRetryDelay):
		t.Fatal("periodic discovery did not apply the current layout color")
	}
}

type fakeDiscovery struct {
	mu      sync.Mutex
	devices []devices.Instance
}

func (discovery *fakeDiscovery) Discover(context.Context) (devices.Discovery, error) {
	discovery.mu.Lock()
	defer discovery.mu.Unlock()
	return devices.Discovery{Devices: discovery.devices}, nil
}

func (discovery *fakeDiscovery) connect(sub *fakeDeviceSubscription, found ...devices.Instance) {
	discovery.mu.Lock()
	discovery.devices = found
	discovery.mu.Unlock()
	sub.events <- struct{}{}
}

type fakeDeviceSubscription struct {
	events chan struct{}
	closed bool
}

func (sub *fakeDeviceSubscription) Events() <-chan struct{} { return sub.events }
func (sub *fakeDeviceSubscription) Close() error            { sub.closed = true; return nil }

type blockedDevice struct {
	*fakeDevice
	started chan struct{}
	release chan struct{}
	err     error
}

func (device *blockedDevice) SetColor(ctx context.Context, _ color.RGB) error {
	close(device.started)
	select {
	case <-device.release:
		return device.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func startRuntime(t *testing.T, runtime *Runtime) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	stop := sync.OnceFunc(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("runtime did not stop")
		}
	})
	t.Cleanup(stop)
	return stop
}

func await(t *testing.T, event <-chan struct{}) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runtime event")
	}
}

func awaitClosed(t *testing.T, device *fakeDevice) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !device.isClosed() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for device to close")
		}
		time.Sleep(time.Millisecond)
	}
}
