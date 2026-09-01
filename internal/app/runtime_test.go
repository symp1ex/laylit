package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"evision-rgb/internal/color"
	"evision-rgb/internal/config"
	"evision-rgb/internal/devices"
	"evision-rgb/internal/layouts"
)

func TestRuntimeAppliesInitialAndSequentialLayoutChanges(t *testing.T) {
	source := newFakeSource("en")
	device := newFakeDevice()
	repository := &fakeRepository{value: config.Config{Version: 1, Layouts: map[string]config.LayoutSettings{
		"en": {Name: "English", Color: "#112233"},
		"ru": {Name: "Russian", Color: "#445566"},
	}}, exists: true}
	runtime := Runtime{Layouts: source, Config: repository, Devices: fakeOpener{device: device}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	assertColor(t, device.applied, color.RGB{R: 0x11, G: 0x22, B: 0x33})
	source.subscription.events <- layouts.Layout{ID: "en", Name: "English"}
	assertNoColor(t, device.applied)
	source.subscription.events <- layouts.Layout{ID: "ru", Name: "Russian"}
	assertColor(t, device.applied, color.RGB{R: 0x44, G: 0x55, B: 0x66})
	source.subscription.events <- layouts.Layout{ID: "en", Name: "English"}
	assertColor(t, device.applied, color.RGB{R: 0x11, G: 0x22, B: 0x33})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !device.isClosed() || !source.subscription.isClosed() {
		t.Fatal("runtime did not close device and subscription")
	}
}

func TestRuntimeUnknownLayoutUsesWhiteAndReports(t *testing.T) {
	source := newFakeSource("en")
	device := newFakeDevice()
	repository := configuredRepository()
	reported := make(chan error, 1)
	runtime := Runtime{Layouts: source, Config: repository, Devices: fakeOpener{device: device}, ReportError: func(err error) { reported <- err }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	assertColor(t, device.applied, color.RGB{R: 1, G: 2, B: 3})
	source.subscription.events <- layouts.Layout{ID: "new", Name: "New"}
	assertColor(t, device.applied, color.RGB{R: 0xFF, G: 0xFF, B: 0xFF})
	select {
	case <-reported:
	case <-time.After(time.Second):
		t.Fatal("unknown layout was not reported")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeSetColorEventErrorIsNonFatal(t *testing.T) {
	source := newFakeSource("en")
	device := newFakeDevice()
	device.failOnCall = 2
	reported := make(chan error, 1)
	runtime := Runtime{Layouts: source, Config: configuredRepository(), Devices: fakeOpener{device: device}, ReportError: func(err error) { reported <- err }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	assertColor(t, device.applied, color.RGB{R: 1, G: 2, B: 3})
	source.subscription.events <- layouts.Layout{ID: "ru"}
	select {
	case <-reported:
	case <-time.After(time.Second):
		t.Fatal("SetColor error was not reported")
	}
	source.subscription.events <- layouts.Layout{ID: "ru"}
	assertColor(t, device.applied, color.RGB{R: 4, G: 5, B: 6})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeStartupFailuresCloseAcquiredDevice(t *testing.T) {
	t.Run("device not found", func(t *testing.T) {
		runtime := Runtime{Layouts: newFakeSource("en"), Config: configuredRepository(), Devices: fakeOpener{err: devices.ErrNotFound}}
		if err := runtime.Run(context.Background()); !errors.Is(err, devices.ErrNotFound) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("initial color failure", func(t *testing.T) {
		device := newFakeDevice()
		device.failOnCall = 1
		runtime := Runtime{Layouts: newFakeSource("en"), Config: configuredRepository(), Devices: fakeOpener{device: device}}
		if err := runtime.Run(context.Background()); err == nil {
			t.Fatal("initial SetColor failure ignored")
		}
		if !device.isClosed() {
			t.Fatal("device not closed after initial SetColor failure")
		}
	})
	t.Run("subscribe failure", func(t *testing.T) {
		source := newFakeSource("en")
		source.subscribeErr = errors.New("listener failed")
		device := newFakeDevice()
		runtime := Runtime{Layouts: source, Config: configuredRepository(), Devices: fakeOpener{device: device}}
		if err := runtime.Run(context.Background()); err == nil {
			t.Fatal("subscribe failure ignored")
		}
		if !device.isClosed() {
			t.Fatal("device not closed after subscribe failure")
		}
	})
}

func TestRuntimeResynchronizesAfterSubscriptionRegistration(t *testing.T) {
	source := newFakeSource("en")
	source.currents = []layouts.Layout{{ID: "en", Name: "English"}, {ID: "ru", Name: "Russian"}}
	device := newFakeDevice()
	runtime := Runtime{Layouts: source, Config: configuredRepository(), Devices: fakeOpener{device: device}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	assertColor(t, device.applied, color.RGB{R: 1, G: 2, B: 3})
	assertColor(t, device.applied, color.RGB{R: 4, G: 5, B: 6})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeCreatesAndReconcilesConfigOnce(t *testing.T) {
	source := newFakeSource("en")
	repository := &fakeRepository{value: config.New()}
	device := newFakeDevice()
	runtime := Runtime{Layouts: source, Config: repository, Devices: fakeOpener{device: device}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	assertColor(t, device.applied, color.RGB{R: 0xFF, G: 0xFF, B: 0xFF})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if repository.saves != 1 || repository.value.Layouts["en"].Color != config.DefaultColor {
		t.Fatalf("repository = %#v saves=%d", repository.value, repository.saves)
	}
}

func TestRuntimeDoesNotRewriteUnchangedConfig(t *testing.T) {
	source := newFakeSource("en")
	source.list[0].Name = "English"
	source.current.Name = "English"
	repository := configuredRepository()
	device := newFakeDevice()
	runtime := Runtime{Layouts: source, Config: repository, Devices: fakeOpener{device: device}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	assertColor(t, device.applied, color.RGB{R: 1, G: 2, B: 3})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if repository.saves != 0 {
		t.Fatalf("unchanged config saved %d times", repository.saves)
	}
}

func configuredRepository() *fakeRepository {
	return &fakeRepository{value: config.Config{Version: 1, Layouts: map[string]config.LayoutSettings{
		"en": {Name: "English", Color: "#010203"},
		"ru": {Name: "Russian", Color: "#040506"},
	}}, exists: true}
}

type fakeRepository struct {
	value  config.Config
	exists bool
	saves  int
}

func (repository *fakeRepository) Load(context.Context) (config.Config, bool, error) {
	return repository.value, repository.exists, nil
}
func (repository *fakeRepository) Save(_ context.Context, value config.Config) error {
	repository.value, repository.exists = value, true
	repository.saves++
	return nil
}

type fakeSource struct {
	list         []layouts.Layout
	current      layouts.Layout
	subscription *fakeSubscription
	subscribeErr error
	currents     []layouts.Layout
	currentCalls int
}

func newFakeSource(id string) *fakeSource {
	current := layouts.Layout{ID: id, Name: id}
	return &fakeSource{list: []layouts.Layout{current}, current: current, subscription: &fakeSubscription{events: make(chan layouts.Layout, 8), errs: make(chan error, 1)}}
}
func (source *fakeSource) List(context.Context) ([]layouts.Layout, error) { return source.list, nil }

func (source *fakeSource) Current(context.Context) (layouts.Layout, error) {
	if source.currentCalls < len(source.currents) {
		current := source.currents[source.currentCalls]
		source.currentCalls++
		return current, nil
	}
	return source.current, nil
}
func (source *fakeSource) Subscribe(context.Context) (layouts.Subscription, error) {
	if source.subscribeErr != nil {
		return nil, source.subscribeErr
	}
	return source.subscription, nil
}

type fakeSubscription struct {
	events chan layouts.Layout
	errs   chan error
	mu     sync.Mutex
	closed bool
}

func (subscription *fakeSubscription) Events() <-chan layouts.Layout { return subscription.events }
func (subscription *fakeSubscription) Errors() <-chan error          { return subscription.errs }
func (subscription *fakeSubscription) Close() error {
	subscription.mu.Lock()
	defer subscription.mu.Unlock()
	subscription.closed = true
	return nil
}
func (subscription *fakeSubscription) isClosed() bool {
	subscription.mu.Lock()
	defer subscription.mu.Unlock()
	return subscription.closed
}

type fakeOpener struct {
	device devices.RGBDevice
	err    error
}

func (opener fakeOpener) Open(context.Context) (devices.RGBDevice, error) {
	return opener.device, opener.err
}

type fakeDevice struct {
	mu         sync.Mutex
	applied    chan color.RGB
	calls      int
	failOnCall int
	closed     bool
}

func newFakeDevice() *fakeDevice { return &fakeDevice{applied: make(chan color.RGB, 8)} }
func (device *fakeDevice) SetColor(_ context.Context, value color.RGB) error {
	device.mu.Lock()
	defer device.mu.Unlock()
	device.calls++
	if device.calls == device.failOnCall {
		return errors.New("HID failed")
	}
	device.applied <- value
	return nil
}
func (device *fakeDevice) Off(context.Context) error { return nil }
func (device *fakeDevice) Close() error {
	device.mu.Lock()
	defer device.mu.Unlock()
	device.closed = true
	return nil
}
func (device *fakeDevice) isClosed() bool {
	device.mu.Lock()
	defer device.mu.Unlock()
	return device.closed
}

func assertColor(t *testing.T, channel <-chan color.RGB, want color.RGB) {
	t.Helper()
	select {
	case got := <-channel:
		if got != want {
			t.Fatalf("color = %v, want %v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for color %v", want)
	}
}

func assertNoColor(t *testing.T, channel <-chan color.RGB) {
	t.Helper()
	select {
	case got := <-channel:
		t.Fatalf("unexpected color %v", got)
	case <-time.After(50 * time.Millisecond):
	}
}
