package hid

import "context"

// Info describes one top-level HID collection.
type Info struct {
	Path                string
	VendorID            uint16
	ProductID           uint16
	Release             uint16
	Interface           int
	UsagePage           uint16
	Usage               uint16
	Serial              string
	Manufacturer        string
	Product             string
	InputReportLength   uint16
	OutputReportLength  uint16
	FeatureReportLength uint16
}

// Connection is an opened HID collection. Implementations must honor context
// cancellation for blocking reads and writes.
type Connection interface {
	InputReportLength() int
	OutputReportLength() int
	Write(ctx context.Context, report []byte) (int, error)
	Read(ctx context.Context, report []byte) (int, error)
	Close() error
}

// Transport enumerates and opens HID collections without knowing any device
// protocol.
type Transport interface {
	Enumerate(ctx context.Context, vendorID, productID uint16) ([]Info, error)
	Open(ctx context.Context, path string) (Connection, error)
}
