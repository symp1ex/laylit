package sessionipc

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	ProtocolVersion uint8 = 1

	TypeHello uint8 = iota + 1
	TypeReady
	TypeStop
	TypeLog
)

const (
	MaxFrameSize       = 16 << 20
	MaxMetadataBytes   = 1 << 20
	MaxLogMessageBytes = 4 << 10
	writeChunkBytes    = 64 << 10
)

type Frame struct {
	Version   uint8
	Type      uint8
	Flags     uint16
	RequestID uint64
	Metadata  json.RawMessage
	Payload   []byte
}

type Handler func(ctx context.Context, frame Frame)

type writeDeadliner interface {
	SetWriteDeadline(time.Time) error
}

type Conn struct {
	rw      io.ReadWriteCloser
	writeMu sync.Mutex

	handler Handler
	closed  chan struct{}
	once    sync.Once
}

func NewConn(rw io.ReadWriteCloser, handler Handler) *Conn {
	return &Conn{
		rw:      rw,
		handler: handler,
		closed:  make(chan struct{}),
	}
}

func (c *Conn) Close() error {
	var err error
	c.once.Do(func() {
		close(c.closed)
		err = c.rw.Close()
	})
	return err
}

func (c *Conn) Closed() <-chan struct{} {
	return c.closed
}

func (c *Conn) Serve(ctx context.Context) error {
	for {
		frame, err := ReadFrame(c.rw)
		if err != nil {
			_ = c.Close()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if c.handler != nil {
			go c.handler(ctx, frame)
		}
	}
}

func (c *Conn) Send(frame Frame) error {
	return c.send(context.Background(), frame)
}

func (c *Conn) SendContext(ctx context.Context, frame Frame) error {
	return c.send(ctx, frame)
}

func (c *Conn) send(ctx context.Context, frame Frame) error {
	c.writeMu.Lock()
	writeErr, resetDeadlineErr := c.writeFrameLocked(ctx, frame)
	c.writeMu.Unlock()
	return reportSendResult(writeErr, resetDeadlineErr)
}

func (c *Conn) TrySendContext(ctx context.Context, frame Frame) (bool, error) {
	if !c.writeMu.TryLock() {
		return false, nil
	}
	writeErr, resetDeadlineErr := c.writeFrameLocked(ctx, frame)
	c.writeMu.Unlock()
	return true, reportSendResult(writeErr, resetDeadlineErr)
}

func (c *Conn) writeFrameLocked(ctx context.Context, frame Frame) (error, error) {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return ctx.Err(), nil
		default:
		}
		if deadline, ok := ctx.Deadline(); ok {
			if deadliner, ok := c.rw.(writeDeadliner); ok {
				if err := deadliner.SetWriteDeadline(deadline); err != nil {
					return fmt.Errorf("set session pipe write deadline: %w", err), nil
				}
				writeErr := WriteFrame(c.rw, frame)
				resetDeadlineErr := deadliner.SetWriteDeadline(time.Time{})
				return writeErr, resetDeadlineErr
			}
		}
	}
	return WriteFrame(c.rw, frame), nil
}

func reportSendResult(writeErr, resetDeadlineErr error) error {
	_ = resetDeadlineErr
	if writeErr != nil {
		return writeErr
	}
	return nil
}

func NewFrame(typ uint8, flags uint16, requestID uint64, meta any, payload []byte) (Frame, error) {
	raw, err := marshalMetadata(meta)
	if err != nil {
		return Frame{}, err
	}
	return Frame{
		Version:   ProtocolVersion,
		Type:      typ,
		Flags:     flags,
		RequestID: requestID,
		Metadata:  raw,
		Payload:   append([]byte(nil), payload...),
	}, nil
}

func ReadFrame(r io.Reader) (Frame, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return Frame{}, err
	}
	size := binary.LittleEndian.Uint32(prefix[:])
	if size < 16 || size > MaxFrameSize {
		return Frame{}, fmt.Errorf("invalid session frame size: %d", size)
	}
	raw := make([]byte, size)
	if _, err := io.ReadFull(r, raw); err != nil {
		return Frame{}, err
	}
	frame := Frame{
		Version:   raw[0],
		Type:      raw[1],
		Flags:     binary.LittleEndian.Uint16(raw[2:4]),
		RequestID: binary.LittleEndian.Uint64(raw[4:12]),
	}
	metaLen := binary.LittleEndian.Uint32(raw[12:16])
	if metaLen > MaxMetadataBytes || int(metaLen) > len(raw)-16 {
		return Frame{}, fmt.Errorf("invalid session metadata size: %d", metaLen)
	}
	frame.Metadata = append([]byte(nil), raw[16:16+metaLen]...)
	frame.Payload = append([]byte(nil), raw[16+metaLen:]...)
	if frame.Version != ProtocolVersion {
		return Frame{}, fmt.Errorf("unsupported session protocol version: %d", frame.Version)
	}
	return frame, nil
}

func WriteFrame(w io.Writer, frame Frame) error {
	if frame.Version == 0 {
		frame.Version = ProtocolVersion
	}
	if frame.Version != ProtocolVersion {
		return fmt.Errorf("unsupported session protocol version: %d", frame.Version)
	}
	if len(frame.Metadata) > MaxMetadataBytes {
		return fmt.Errorf("session metadata too large: %d", len(frame.Metadata))
	}
	size := 16 + len(frame.Metadata) + len(frame.Payload)
	if size > MaxFrameSize {
		return fmt.Errorf("session frame too large: %d", size)
	}
	var header [20]byte
	binary.LittleEndian.PutUint32(header[0:4], uint32(size))
	header[4] = frame.Version
	header[5] = frame.Type
	binary.LittleEndian.PutUint16(header[6:8], frame.Flags)
	binary.LittleEndian.PutUint64(header[8:16], frame.RequestID)
	binary.LittleEndian.PutUint32(header[16:20], uint32(len(frame.Metadata)))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	if err := writeAll(w, frame.Metadata); err != nil {
		return err
	}
	return writeAll(w, frame.Payload)
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		chunk := data
		if len(chunk) > writeChunkBytes {
			chunk = chunk[:writeChunkBytes]
		}
		n, err := w.Write(chunk)
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func DecodeMetadata[T any](frame Frame) (T, error) {
	var out T
	if len(frame.Metadata) == 0 {
		return out, nil
	}
	err := json.Unmarshal(frame.Metadata, &out)
	return out, err
}

func marshalMetadata(meta any) (json.RawMessage, error) {
	if meta == nil {
		return nil, nil
	}
	if raw, ok := meta.(json.RawMessage); ok {
		return raw, nil
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

type HelloMeta struct {
	Nonce     string `json:"nonce"`
	PID       int    `json:"pid"`
	SessionID uint32 `json:"windows_session_id"`
	Role      string `json:"role"`
}

type LogMeta struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}
