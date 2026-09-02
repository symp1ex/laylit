package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const queueSize = 4096

type PlainHandler struct {
	w     io.Writer
	level slog.Level

	queue chan logEntry
	stop  chan struct{}
	done  chan struct{}

	stopping sync.Once
	closed   atomic.Bool
	dropped  atomic.Uint64
}

type logEntry struct {
	when   time.Time
	level  slog.Level
	msg    string
	format string
	args   []any
	flush  chan struct{}
}

func NewPlainHandler(w io.Writer, level slog.Level) *PlainHandler {
	h := &PlainHandler{
		w:     w,
		level: level,
		queue: make(chan logEntry, queueSize),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go h.run()
	return h
}

func (h *PlainHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *PlainHandler) Handle(_ context.Context, r slog.Record) error {
	h.enqueue(logEntry{
		when:  r.Time,
		level: r.Level,
		msg:   r.Message,
	})
	return nil
}

func (h *PlainHandler) EnqueueFormat(level slog.Level, format string, args ...any) {
	if !h.Enabled(context.Background(), level) {
		return
	}
	h.enqueue(logEntry{
		when:   time.Now(),
		level:  level,
		format: format,
		args:   args,
	})
}

func (h *PlainHandler) Flush(timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = time.Second
	}
	done := make(chan struct{})
	if !h.enqueue(logEntry{flush: done}) {
		return false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (h *PlainHandler) Shutdown(timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = time.Second
	}
	h.stopping.Do(func() {
		h.closed.Store(true)
		close(h.stop)
	})
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-h.done:
		return true
	case <-timer.C:
		return false
	}
}

func (h *PlainHandler) enqueue(entry logEntry) bool {
	if h == nil || h.closed.Load() {
		return false
	}
	select {
	case h.queue <- entry:
		return true
	default:
		h.dropped.Add(1)
		return false
	}
}

func (h *PlainHandler) run() {
	defer close(h.done)

	for {
		select {
		case entry := <-h.queue:
			h.handle(entry)
		case <-h.stop:
			for {
				select {
				case entry := <-h.queue:
					h.handle(entry)
				default:
					h.writeDropped()
					if closer, ok := h.w.(io.Closer); ok {
						_ = closer.Close()
					}
					return
				}
			}
		}
	}
}

func (h *PlainHandler) handle(entry logEntry) {
	if entry.flush != nil {
		h.writeDropped()
		close(entry.flush)
		return
	}

	h.writeDropped()
	msg := entry.msg
	if entry.format != "" {
		msg = fmt.Sprintf(entry.format, entry.args...)
	}
	h.writeLine(entry.when, entry.level, msg)
}

func (h *PlainHandler) writeDropped() {
	n := h.dropped.Swap(0)
	if n == 0 {
		return
	}
	h.writeLine(time.Now(), slog.LevelWarn, fmt.Sprintf("logger dropped %d messages because the queue is full", n))
}

func (h *PlainHandler) writeLine(t time.Time, level slog.Level, msg string) {
	if t.IsZero() {
		t = time.Now()
	}
	ts := t.Format("2006-01-02 15:04:05,000")
	lvl := strings.ToUpper(level.String())
	line := fmt.Sprintf("[%s] [%s] %s\n", ts, lvl, msg)
	_, _ = h.w.Write([]byte(line))
}

func (h *PlainHandler) WithAttrs(_ []slog.Attr) slog.Handler {
	return h
}

func (h *PlainHandler) WithGroup(_ string) slog.Handler {
	return h
}
