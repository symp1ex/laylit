package winio

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// Overlapped waits for completion even after cancellation so the driver
// cannot retain pointers to the operation's buffers or OVERLAPPED structure.
func Overlapped(ctx context.Context, handle windows.Handle, start func(*uint32, *windows.Overlapped) error) (uint32, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.Close(event)
	operation := &windows.Overlapped{HEvent: event}
	var transferred uint32
	err = start(&transferred, operation)
	if !errors.Is(err, windows.ERROR_IO_PENDING) {
		return transferred, err
	}

	cancelDone := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() {
		cancelDone <- windows.CancelIoEx(handle, operation)
	})
	err = windows.GetOverlappedResult(handle, operation, &transferred, true)
	if !stop() {
		cancelErr := <-cancelDone
		if cancelErr != nil && !errors.Is(cancelErr, windows.ERROR_NOT_FOUND) {
			err = errors.Join(err, fmt.Errorf("cancel I/O: %w", cancelErr))
		}
		if errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
			err = errors.Join(ctx.Err(), err)
		}
	}
	return transferred, err
}
