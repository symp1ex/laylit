package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"laylit/internal/color"
	"laylit/internal/config"
	"laylit/internal/devices"
	"laylit/internal/layouts"
)

type DeviceDiscovery interface {
	Discover(ctx context.Context) (devices.Discovery, error)
}

type DeviceSubscription interface {
	Events() <-chan struct{}
	Close() error
}

type Runtime struct {
	Layouts      layouts.Source
	Config       config.Repository
	Devices      DeviceDiscovery
	WatchDevices func() (DeviceSubscription, error)
	ReportError  func(error)
	Infof        func(string, ...any)
	Tracef       func(string, ...any)
	Ready        func() error
	DefaultColor color.RGB
}

func (runtime *Runtime) Run(ctx context.Context) (returnErr error) {
	installed, err := runtime.Layouts.List(ctx)
	if err != nil {
		return fmt.Errorf("discover Windows keyboard layouts: %w", err)
	}
	active, err := runtime.Layouts.Current(ctx)
	if err != nil {
		return fmt.Errorf("determine active Windows keyboard layout: %w", err)
	}
	settings, exists, err := runtime.Config.Load(ctx)
	if err != nil {
		return fmt.Errorf("load layout config: %w", err)
	}
	settings, changed, err := config.Reconcile(settings, includeActive(installed, active))
	if err != nil {
		return fmt.Errorf("reconcile layout config: %w", err)
	}

	subscription, err := runtime.Layouts.Subscribe(ctx)
	if err != nil {
		return fmt.Errorf("subscribe to Windows layout changes: %w", err)
	}
	defer func() {
		if err := subscription.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close layout subscription: %w", err))
		}
	}()
	// A change before this read is observed here; a later change is queued.
	active, err = runtime.Layouts.Current(ctx)
	if err != nil {
		return fmt.Errorf("re-read active layout after subscription: %w", err)
	}

	var deviceEvents <-chan struct{}
	if runtime.WatchDevices != nil {
		notifications, err := runtime.WatchDevices()
		if err != nil {
			return fmt.Errorf("subscribe to Windows device changes: %w", err)
		}
		defer func() {
			if err := notifications.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close device subscription: %w", err))
			}
		}()
		deviceEvents = notifications.Events()
	}

	ctx, cancel := context.WithCancel(ctx)
	workers := make(map[string]*deviceWorker)
	retiring := make(map[string]<-chan struct{})
	defer func() {
		cancel()
		for _, worker := range workers {
			<-worker.done
		}
		for _, done := range retiring {
			<-done
		}
	}()

	discovery, discoveryErr := runtime.Devices.Discover(ctx)
	dirty := !exists || changed
	for _, instance := range discovery.Devices {
		dirty = settings.AddDevice(instance.ID) || dirty
	}
	if dirty {
		if err := runtime.Config.Save(ctx, settings); err != nil {
			return fmt.Errorf("save reconciled layout config: %w", err)
		}
		dirty = false
	}

	runtime.reconcileDevices(ctx, workers, retiring, discovery.Devices, settings, active)
	if runtime.Ready != nil {
		if err := runtime.Ready(); err != nil {
			return fmt.Errorf("report runtime readiness: %w", err)
		}
	}

	type scan struct {
		discovery devices.Discovery
		err       error
	}
	var scanning <-chan scan
	defer func() {
		cancel()
		if scanning != nil {
			<-scanning
		}
	}()
	startScan := func() {
		results := make(chan scan, 1)
		scanning = results
		go func() {
			discovery, err := runtime.Devices.Discover(ctx)
			results <- scan{discovery: discovery, err: err}
		}()
	}
	retry := time.NewTicker(deviceRetryDelay)
	defer retry.Stop()
	reportDiscovery := func(err error) {
		if err != nil && ctx.Err() == nil {
			runtime.report(fmt.Errorf("discover RGB devices: %w", err))
		}
	}
	reportDiscovery(discoveryErr)

	events, errorsChannel := subscription.Events(), subscription.Errors()
	for events != nil || errorsChannel != nil {
		// While a scan is active, leave further notifications queued.
		changes := deviceEvents
		if scanning != nil {
			changes = nil
		}
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-changes:
			if ok {
				startScan()
			} else {
				deviceEvents = nil
				runtime.report(errors.New("Windows device subscription stopped unexpectedly"))
			}
		case result := <-scanning:
			scanning = nil
			if discoveryErr == nil || result.err == nil || discoveryErr.Error() != result.err.Error() {
				reportDiscovery(result.err)
			}
			discoveryErr = result.err
			for _, instance := range result.discovery.Devices {
				dirty = settings.AddDevice(instance.ID) || dirty
			}
			runtime.reconcileDevices(ctx, workers, retiring, result.discovery.Devices, settings, active)
		case <-retry.C:
			if discoveryErr != nil && scanning == nil {
				startScan()
			}
		case layout, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			runtime.trace("runtime event received layout_id=%s applied_layout_id=%s", layout.ID, active.ID)
			if layout.ID == active.ID {
				runtime.trace("runtime event layout_id=%s result=deduplicated", layout.ID)
				continue
			}
			active = layout
			if _, known, _ := settings.Color(layout.ID); !known {
				runtime.report(fmt.Errorf("active layout %q is absent from config; applying per-device colors or safe default %s until restart", layout.ID, runtime.defaultColor()))
			}
			for id, worker := range workers {
				runtime.queueColor(worker, id, settings, active)
			}
		case notificationErr, ok := <-errorsChannel:
			if !ok {
				errorsChannel = nil
				continue
			}
			runtime.report(notificationErr)
		}
		if dirty && ctx.Err() == nil {
			if err := runtime.Config.Save(ctx, settings); err != nil {
				runtime.report(fmt.Errorf("save reconciled device config: %w", err))
			} else {
				dirty = false
			}
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	return errors.New("Windows layout subscription stopped unexpectedly")
}

func (runtime *Runtime) reconcileDevices(ctx context.Context, workers map[string]*deviceWorker, retiring map[string]<-chan struct{}, found []devices.Instance, settings config.Config, active layouts.Layout) {
	for id, done := range retiring {
		select {
		case <-done:
			delete(retiring, id)
		default:
		}
	}
	present := make(map[string]devices.Instance, len(found))
	for _, instance := range found {
		present[instance.ID] = instance
	}
	for id, worker := range workers {
		instance, exists := present[id]
		if !exists || instance.Location != worker.location {
			runtime.info("RGB device disconnected; device_id=%s", id)
			worker.cancel()
			retiring[id] = worker.done
			delete(workers, id)
		}
	}
	for _, instance := range found {
		id := instance.ID
		if workers[id] != nil {
			continue
		}
		if !settings.Devices[id].Enabled {
			runtime.trace("RGB device disabled; device_id=%s", id)
			continue
		}
		runtime.info("RGB device detected; device_id=%s device=%s", id, instance.Name)
		workerCtx, cancel := context.WithCancel(ctx)
		worker := &deviceWorker{location: instance.Location, cancel: cancel, done: make(chan struct{}), pending: make(chan deviceColor, 1)}
		workers[id] = worker
		runtime.queueColor(worker, id, settings, active)
		previous := retiring[id]
		delete(retiring, id)
		go runtime.runDevice(workerCtx, instance, worker, previous)
	}
}

func (runtime *Runtime) queueColor(worker *deviceWorker, id string, settings config.Config, layout layouts.Layout) {
	selected, known, err := settings.DeviceColor(id, layout.ID, runtime.defaultColor())
	if err != nil {
		runtime.report(fmt.Errorf("select RGB device %s color for layout %q: %w", id, layout.ID, err))
		return
	}
	runtime.trace("runtime config lookup layout_id=%s known=%t selected_rgb=%s device_id=%s", layout.ID, known, selected, id)
	worker.update(deviceColor{layoutID: layout.ID, rgb: selected})
}

func includeActive(installed []layouts.Layout, active layouts.Layout) []layouts.Layout {
	for _, layout := range installed {
		if layout.ID == active.ID {
			return installed
		}
	}
	return append(installed, active)
}

func (runtime *Runtime) defaultColor() color.RGB {
	if runtime.DefaultColor == (color.RGB{}) {
		return color.RGB{R: 0xFF, G: 0xFF, B: 0xFF}
	}
	return runtime.DefaultColor
}

func (runtime *Runtime) report(err error) {
	if runtime.ReportError != nil {
		runtime.ReportError(err)
	}
}

func (runtime *Runtime) info(format string, args ...any) {
	if runtime.Infof != nil {
		runtime.Infof(format, args...)
	}
}

func (runtime *Runtime) trace(format string, args ...any) {
	if runtime.Tracef != nil {
		runtime.Tracef(format, args...)
	}
}
