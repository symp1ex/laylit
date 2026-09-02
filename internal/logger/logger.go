package logger

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var (
	loggers  = make(map[string]*slog.Logger)
	handlers = make(map[string]*PlainHandler)
	mu       sync.Mutex

	Laylit Logger

	logDir     = "logs"
	retainDays = 14
	logLevel   = "INFO"
)

type Logger struct {
	*slog.Logger
	handler *PlainHandler
}

type callbackHandler struct {
	level slog.Level
	sink  func(level, message string)
}

func Configure(dir string, level string, retain int) {
	mu.Lock()
	oldHandlers := handlers
	if dir != "" {
		logDir = dir
	}
	if level != "" {
		logLevel = level
	}
	if retain > 0 {
		retainDays = retain
	}

	loggers = make(map[string]*slog.Logger)
	handlers = make(map[string]*PlainHandler)
	mu.Unlock()

	shutdownHandlers(oldHandlers, 2*time.Second)

	Laylit = newLogger("laylit")
}

func ConfigureRemote(level string, sink func(level, message string)) {
	handler := &callbackHandler{
		level: levelFromString(level),
		sink:  sink,
	}
	Laylit = Logger{Logger: slog.New(handler)}
}

func levelFromString(level string) slog.Level {
	switch strings.ToUpper(level) {
	case "DEBUG":
		return slog.LevelDebug
	case "INFO":
		return slog.LevelInfo
	case "WARNING", "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func (l Logger) Infof(format string, args ...any) {
	l.logf(slog.LevelInfo, format, args...)
}

func (l Logger) Debugf(format string, args ...any) {
	l.logf(slog.LevelDebug, format, args...)
}

func (l Logger) Warnf(format string, args ...any) {
	l.logf(slog.LevelWarn, format, args...)
}

func (l Logger) Errorf(format string, args ...any) {
	l.logf(slog.LevelError, format, args...)
}

func (l Logger) logf(level slog.Level, format string, args ...any) {
	if l.Logger == nil {
		return
	}

	ctx := context.Background()
	if !l.Enabled(ctx, level) {
		return
	}

	if l.handler != nil {
		l.handler.EnqueueFormat(level, format, args...)
		return
	}
	l.Log(ctx, level, fmt.Sprintf(format, args...))
}

func (h *callbackHandler) Enabled(_ context.Context, level slog.Level) bool {
	return h != nil && h.sink != nil && level >= h.level
}

func (h *callbackHandler) Handle(_ context.Context, record slog.Record) error {
	if h != nil && h.sink != nil {
		h.sink(levelName(record.Level), record.Message)
	}
	return nil
}

func (h *callbackHandler) WithAttrs(_ []slog.Attr) slog.Handler {
	return h
}

func (h *callbackHandler) WithGroup(_ string) slog.Handler {
	return h
}

func levelName(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return "error"
	case level >= slog.LevelWarn:
		return "warn"
	case level <= slog.LevelDebug:
		return "debug"
	default:
		return "info"
	}
}

func LogSessionHelperRemote(level, message string, pid int, sessionID uint32, role string) {
	message = normalizeRemoteMessage(message)
	format := "Remote session-helper log: pid=%d windows_session_id=%d role=%s message=%s"
	args := []any{pid, sessionID, role, message}

	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		Laylit.Debugf(format, args...)
	case "info":
		Laylit.Infof(format, args...)
	case "warn":
		Laylit.Warnf(format, args...)
	case "error":
		Laylit.Errorf(format, args...)
	default:
		Laylit.Warnf(format, args...)
	}
}

func normalizeRemoteMessage(message string) string {
	message = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(message)
	raw := []byte(strings.ToValidUTF8(message, "\uFFFD"))
	const maxRemoteMessageBytes = 4 << 10
	if len(raw) <= maxRemoteMessageBytes {
		return string(raw)
	}
	raw = raw[:maxRemoteMessageBytes]
	for len(raw) > 0 && !utf8.Valid(raw) {
		raw = raw[:len(raw)-1]
	}
	return string(raw)
}

func Flush(timeout time.Duration) bool {
	mu.Lock()
	snapshot := make(map[string]*PlainHandler, len(handlers))
	for name, handler := range handlers {
		snapshot[name] = handler
	}
	mu.Unlock()

	ok := true
	for _, handler := range snapshot {
		if !handler.Flush(timeout) {
			ok = false
		}
	}
	return ok
}

func Shutdown(timeout time.Duration) bool {
	mu.Lock()
	snapshot := handlers
	handlers = make(map[string]*PlainHandler)
	loggers = make(map[string]*slog.Logger)
	mu.Unlock()

	return shutdownHandlers(snapshot, timeout)
}

func newLogger(name string) Logger {
	l, h := get(name)
	return Logger{Logger: l, handler: h}
}

func get(name string) (*slog.Logger, *PlainHandler) {
	mu.Lock()
	defer mu.Unlock()

	if l, ok := loggers[name]; ok {
		return l, handlers[name]
	}

	writer := NewRotatingWriter(name)
	handler := NewPlainHandler(writer, levelFromString(logLevel))

	l := slog.New(handler)
	loggers[name] = l
	handlers[name] = handler
	return l, handler
}

func shutdownHandlers(snapshot map[string]*PlainHandler, timeout time.Duration) bool {
	ok := true
	for _, handler := range snapshot {
		if handler != nil && !handler.Shutdown(timeout) {
			ok = false
		}
	}
	return ok
}
