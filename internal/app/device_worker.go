package app

import (
	"context"
	"fmt"
	"time"

	"laylit/internal/color"
	"laylit/internal/devices"
)

const deviceRetryDelay = 2 * time.Second

type deviceColor struct {
	layoutID string
	rgb      color.RGB
}

type deviceWorker struct {
	location string
	cancel   context.CancelFunc
	done     chan struct{}
	pending  chan deviceColor
}

// Only the runtime publishes. The worker consumes independently, keeping at
// most one pending update while it is opening or writing to its keyboard.
func (worker *deviceWorker) update(value deviceColor) {
	select {
	case <-worker.pending:
	default:
	}
	worker.pending <- value
}

func (runtime *Runtime) runDevice(ctx context.Context, instance devices.Instance, worker *deviceWorker, previous <-chan struct{}) {
	defer close(worker.done)
	if previous != nil {
		<-previous
	}

	var connection devices.RGBDevice
	closeDevice := func() {
		if connection != nil {
			if err := connection.Close(); err != nil {
				runtime.report(fmt.Errorf("close RGB device %s: %w", instance.ID, err))
			}
			connection = nil
		}
	}
	defer closeDevice()

	failed := false
	for {
		var value deviceColor
		select {
		case <-ctx.Done():
			return
		case value = <-worker.pending:
		}
		for ctx.Err() == nil {
			if connection == nil {
				var err error
				connection, err = instance.Open(ctx)
				if err != nil {
					if ctx.Err() == nil {
						runtime.report(fmt.Errorf("open RGB device %s: %w", instance.ID, err))
					}
					failed = true
				}
			}
			if connection != nil {
				select {
				case value = <-worker.pending:
				default:
				}
				runtime.trace("runtime SetColor start layout_id=%s rgb=%s device_id=%s", value.layoutID, value.rgb, instance.ID)
				if err := connection.SetColor(ctx, value.rgb); err == nil {
					runtime.trace("runtime SetColor result=success layout_id=%s rgb=%s device_id=%s", value.layoutID, value.rgb, instance.ID)
					if failed {
						runtime.info("RGB device connection restored; device_id=%s", instance.ID)
						failed = false
					}
					break
				} else if ctx.Err() == nil {
					runtime.report(fmt.Errorf("set RGB device %s color to %s for layout %q: %w", instance.ID, value.rgb, value.layoutID, err))
				}
				failed = true
				closeDevice()
			}

			timer := time.NewTimer(deviceRetryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				select {
				case value = <-worker.pending:
				default:
				}
			}
		}
	}
}
