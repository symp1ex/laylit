package evision

import (
	"context"
	"testing"
	"time"

	"evision-rgb/internal/color"
	"evision-rgb/internal/hid"
)

func TestDeviceSetColorWritesExactReportThenReadsAcknowledgement(t *testing.T) {
	connection := &fakeConnection{inputLength: reportSize, outputLength: reportSize}
	device := &Device{connection: connection, debugf: func(string, ...any) {}}
	if err := device.SetColor(context.Background(), color.RGB{R: 0x12, G: 0x34, B: 0x56}); err != nil {
		t.Fatal(err)
	}
	want := buildStaticReport(0x12, 0x34, 0x56, brightnessHighest)
	if string(connection.written) != string(want[:]) {
		t.Fatalf("written report =\n% X\nwant\n% X", connection.written, want)
	}
	if connection.readCalls != 1 {
		t.Fatalf("acknowledgement reads = %d, want 1", connection.readCalls)
	}
	if connection.writeDeadline < 1500*time.Millisecond || connection.writeDeadline > 2100*time.Millisecond {
		t.Fatalf("write deadline = %s, expected approximately 2s", connection.writeDeadline)
	}
	if connection.readDeadline < 500*time.Millisecond || connection.readDeadline > 1100*time.Millisecond {
		t.Fatalf("read deadline = %s, expected approximately 1s", connection.readDeadline)
	}
}

func TestDeviceReportsWriteAndAcknowledgementFailures(t *testing.T) {
	tests := []struct {
		name       string
		connection *fakeConnection
	}{
		{"write error", &fakeConnection{inputLength: reportSize, outputLength: reportSize, writeErr: context.DeadlineExceeded}},
		{"short write", &fakeConnection{inputLength: reportSize, outputLength: reportSize, writeCount: 63}},
		{"read error", &fakeConnection{inputLength: reportSize, outputLength: reportSize, readErr: context.DeadlineExceeded}},
		{"short read", &fakeConnection{inputLength: reportSize, outputLength: reportSize, readCount: 63}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			device := &Device{connection: test.connection, debugf: func(string, ...any) {}}
			if err := device.SetColor(context.Background(), color.RGB{}); err == nil {
				t.Fatal("I/O failure was ignored")
			}
		})
	}
}

func TestProviderUsesExactIDsAndClosesUnsupportedReportSizes(t *testing.T) {
	for _, lengths := range []struct{ input, output int }{{reportSize, 65}, {65, reportSize}} {
		connection := &fakeConnection{inputLength: lengths.input, outputLength: lengths.output}
		transport := &fakeTransport{
			infos:      []hid.Info{{Path: "candidate", Interface: rgbInterface, UsagePage: rgbUsagePage}},
			connection: connection,
		}
		provider := NewProvider(transport, Options{})
		if _, err := provider.Open(context.Background()); err == nil {
			t.Fatal("unsupported report size accepted")
		}
		if transport.vendorID != vendorID || transport.productID != productID {
			t.Fatalf("enumerated %04X:%04X", transport.vendorID, transport.productID)
		}
		if !connection.closed {
			t.Fatal("connection not closed after report-size validation failure")
		}
	}
}

type fakeTransport struct {
	infos      []hid.Info
	connection hid.Connection
	vendorID   uint16
	productID  uint16
}

func (transport *fakeTransport) Enumerate(_ context.Context, vendorID, productID uint16) ([]hid.Info, error) {
	transport.vendorID, transport.productID = vendorID, productID
	return transport.infos, nil
}

func (transport *fakeTransport) Open(context.Context, string) (hid.Connection, error) {
	return transport.connection, nil
}

type fakeConnection struct {
	inputLength, outputLength int
	written                   []byte
	writeErr, readErr         error
	writeCount, readCount     int
	readCalls                 int
	writeDeadline             time.Duration
	readDeadline              time.Duration
	closed                    bool
}

func (connection *fakeConnection) InputReportLength() int  { return connection.inputLength }
func (connection *fakeConnection) OutputReportLength() int { return connection.outputLength }
func (connection *fakeConnection) Write(ctx context.Context, report []byte) (int, error) {
	connection.written = append([]byte(nil), report...)
	connection.writeDeadline = remainingDeadline(ctx)
	if connection.writeErr != nil {
		return 0, connection.writeErr
	}
	if connection.writeCount != 0 {
		return connection.writeCount, nil
	}
	return len(report), nil
}
func (connection *fakeConnection) Read(ctx context.Context, report []byte) (int, error) {
	connection.readCalls++
	connection.readDeadline = remainingDeadline(ctx)
	if connection.readErr != nil {
		return 0, connection.readErr
	}
	if connection.readCount != 0 {
		return connection.readCount, nil
	}
	return len(report), nil
}
func (connection *fakeConnection) Close() error { connection.closed = true; return nil }

func remainingDeadline(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0
	}
	return time.Until(deadline)
}
