package logger

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"laylit/internal/sessionipc"
)

const (
	remoteLogQueueSize   = 256
	remoteLogSendTimeout = 250 * time.Millisecond
)

type RemoteLogSender struct {
	queue chan sessionipc.LogMeta
	wake  chan struct{}
	stop  chan struct{}
	done  chan struct{}

	mu      sync.RWMutex
	conn    *sessionipc.Conn
	enabled bool
	once    sync.Once
}

func NewRemoteLogSender() *RemoteLogSender {
	sender := &RemoteLogSender{
		queue: make(chan sessionipc.LogMeta, remoteLogQueueSize),
		wake:  make(chan struct{}, 1),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go sender.run()
	return sender
}

func (s *RemoteLogSender) Enqueue(level, message string) {
	entry := sessionipc.LogMeta{
		Level:   level,
		Message: truncateLogMessage(message),
	}
	select {
	case s.queue <- entry:
	default:
	}
}

func (s *RemoteLogSender) SetConnection(conn *sessionipc.Conn, enabled bool) {
	s.mu.Lock()
	s.conn = conn
	s.enabled = enabled
	s.mu.Unlock()
	s.signal()
}

func (s *RemoteLogSender) ClearConnection(conn *sessionipc.Conn) {
	s.mu.Lock()
	if s.conn == conn {
		s.conn = nil
		s.enabled = false
	}
	s.mu.Unlock()
	s.signal()
}

func (s *RemoteLogSender) Close() {
	s.once.Do(func() {
		close(s.stop)
		<-s.done
	})
}

func (s *RemoteLogSender) run() {
	defer close(s.done)
	for {
		conn, enabled := s.connection()
		if conn == nil || !enabled {
			select {
			case <-s.wake:
				continue
			case <-s.stop:
				return
			}
		}

		select {
		case entry := <-s.queue:
			frame, err := sessionipc.NewFrame(sessionipc.TypeLog, 0, 0, entry, nil)
			if err != nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), remoteLogSendTimeout)
			_, err = conn.TrySendContext(ctx, frame)
			cancel()
			if err != nil {
				s.ClearConnection(conn)
			}
		case <-s.wake:
		case <-s.stop:
			return
		}
	}
}

func (s *RemoteLogSender) connection() (*sessionipc.Conn, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.conn, s.enabled
}

func (s *RemoteLogSender) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func truncateLogMessage(message string) string {
	raw := []byte(strings.ToValidUTF8(message, "\uFFFD"))
	if len(raw) <= sessionipc.MaxLogMessageBytes {
		return string(raw)
	}
	raw = raw[:sessionipc.MaxLogMessageBytes]
	for len(raw) > 0 && !utf8.Valid(raw) {
		raw = raw[:len(raw)-1]
	}
	return string(raw)
}
