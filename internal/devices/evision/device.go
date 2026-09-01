package evision

import (
	"context"
	"fmt"
	"sync"
	"time"

	"laylit/internal/color"
	"laylit/internal/hid"
)

type Device struct {
	connection hid.Connection
	debugf     func(string, ...any)
	mu         sync.Mutex
	closed     bool
}

func (device *Device) SetColor(ctx context.Context, value color.RGB) error {
	report := buildStaticReport(value.R, value.G, value.B, brightnessHighest)
	return device.sendReport(ctx, report[:])
}

func (device *Device) Off(ctx context.Context) error {
	report := buildStaticReport(0, 0, 0, brightnessOff)
	return device.sendReport(ctx, report[:])
}

func (device *Device) sendReport(ctx context.Context, report []byte) error {
	device.mu.Lock()
	defer device.mu.Unlock()
	if device.closed {
		return errorsNewClosed()
	}
	if len(report) != reportSize {
		return fmt.Errorf("invalid output report size: got %d, want %d", len(report), reportSize)
	}
	device.debugf("output report (%d bytes): %s", len(report), formatHex(report))

	writeContext, cancelWrite := context.WithTimeout(ctx, 2*time.Second)
	written, err := device.connection.Write(writeContext, report)
	cancelWrite()
	if err != nil {
		return fmt.Errorf("write output report: %w", err)
	}
	if written != reportSize {
		return fmt.Errorf("write output report: wrote %d bytes, want %d", written, reportSize)
	}
	device.debugf("write: %d bytes", written)

	readContext, cancelRead := context.WithTimeout(ctx, time.Second)
	response := make([]byte, reportSize)
	read, err := device.connection.Read(readContext, response)
	cancelRead()
	if err != nil {
		return fmt.Errorf("read device acknowledgement: %w", err)
	}
	if read != reportSize {
		return fmt.Errorf("read device acknowledgement: got %d bytes, want %d", read, reportSize)
	}
	device.debugf("read acknowledgement: %d bytes", read)
	return nil
}

func (device *Device) Close() error {
	if device == nil {
		return nil
	}
	device.mu.Lock()
	defer device.mu.Unlock()
	if device.closed {
		return nil
	}
	device.closed = true
	return device.connection.Close()
}

func errorsNewClosed() error { return fmt.Errorf("RGB device is closed") }
