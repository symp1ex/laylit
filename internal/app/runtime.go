package app

import (
	"context"
	"errors"
	"fmt"

	"laylit/internal/color"
	"laylit/internal/config"
	"laylit/internal/devices"
	"laylit/internal/layouts"
)

type DeviceOpener interface {
	Open(ctx context.Context) (devices.RGBDevice, error)
}

type Runtime struct {
	Layouts      layouts.Source
	Config       config.Repository
	Devices      DeviceOpener
	ReportError  func(error)
	Tracef       func(string, ...any)
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
	installed = includeActive(installed, active)

	settings, exists, err := runtime.Config.Load(ctx)
	if err != nil {
		return fmt.Errorf("load layout config: %w", err)
	}
	settings, changed, err := config.Reconcile(settings, installed)
	if err != nil {
		return fmt.Errorf("reconcile layout config: %w", err)
	}
	if !exists || changed {
		if err := runtime.Config.Save(ctx, settings); err != nil {
			return fmt.Errorf("save reconciled layout config: %w", err)
		}
	}

	device, err := runtime.Devices.Open(ctx)
	if err != nil {
		return fmt.Errorf("discover/open RGB device: %w", err)
	}
	defer func() {
		if err := device.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close RGB device: %w", err))
		}
	}()

	if err := applyLayout(ctx, device, settings, active, runtime.defaultColor(), runtime.trace); err != nil {
		return fmt.Errorf("apply initial layout %q: %w", active.ID, err)
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

	// Registration happens before this second read. A change before the read is
	// observed here; a change after it is queued by the subscription.
	resynchronized, err := runtime.Layouts.Current(ctx)
	if err != nil {
		return fmt.Errorf("re-read active layout after subscription: %w", err)
	}
	appliedLayoutID := active.ID
	if resynchronized.ID != appliedLayoutID {
		if err := applyLayout(ctx, device, settings, resynchronized, runtime.defaultColor(), runtime.trace); err != nil {
			return fmt.Errorf("apply active layout after subscription %q: %w", resynchronized.ID, err)
		}
		appliedLayoutID = resynchronized.ID
	}

	events := subscription.Events()
	errorsChannel := subscription.Errors()
	for events != nil || errorsChannel != nil {
		select {
		case <-ctx.Done():
			return nil
		case layout, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			runtime.trace("runtime event received layout_id=%s applied_layout_id=%s", layout.ID, appliedLayoutID)
			if layout.ID == appliedLayoutID {
				runtime.trace("runtime event layout_id=%s result=deduplicated", layout.ID)
				continue
			}
			if _, known, _ := settings.Color(layout.ID); !known {
				runtime.report(fmt.Errorf("active layout %q is absent from config; applying safe default %s until restart", layout.ID, runtime.defaultColor()))
			}
			if err := applyLayout(ctx, device, settings, layout, runtime.defaultColor(), runtime.trace); err != nil {
				if ctx.Err() == nil {
					runtime.report(fmt.Errorf("apply layout change %q: %w", layout.ID, err))
				}
				continue
			}
			appliedLayoutID = layout.ID
		case notificationErr, ok := <-errorsChannel:
			if !ok {
				errorsChannel = nil
				continue
			}
			runtime.report(notificationErr)
		}
	}
	return errors.New("Windows layout subscription stopped unexpectedly")
}

func applyLayout(ctx context.Context, device devices.RGBDevice, settings config.Config, layout layouts.Layout, fallback color.RGB, tracef func(string, ...any)) error {
	selected, known, err := settings.Color(layout.ID)
	if err != nil {
		return err
	}
	if !known {
		selected = fallback
	}
	if tracef != nil {
		tracef("runtime config lookup layout_id=%s known=%t selected_rgb=%s", layout.ID, known, selected)
		tracef("runtime SetColor start layout_id=%s rgb=%s", layout.ID, selected)
	}
	if err := device.SetColor(ctx, selected); err != nil {
		if tracef != nil {
			tracef("runtime SetColor result=error layout_id=%s rgb=%s error=%q", layout.ID, selected, err)
		}
		return fmt.Errorf("set device color to %s: %w", selected, err)
	}
	if tracef != nil {
		tracef("runtime SetColor result=success layout_id=%s rgb=%s", layout.ID, selected)
	}
	return nil
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

func (runtime *Runtime) trace(format string, args ...any) {
	if runtime.Tracef != nil {
		runtime.Tracef(format, args...)
	}
}
