package entry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"laylit/internal/logger"
	"laylit/internal/logsettings"
	"laylit/internal/sessionipc"
)

const sessionHelperRole = "session-helper"

type helperConnection interface {
	io.Reader
	io.Writer
	io.Closer
}

type automaticRunner func(context.Context, func() error) error

func runSessionHelper(pipeName, nonce string) error {
	settings, settingsPath, err := logsettings.Load()
	if err != nil {
		return err
	}
	sessionID, err := currentProcessSessionID()
	if err != nil {
		return err
	}
	remoteLogs := logger.NewRemoteLogSender()
	defer remoteLogs.Close()
	logger.ConfigureRemote(settings.LogLevel, remoteLogs.Enqueue)
	logger.Laylit.Infof("session helper starting; settings=%s", settingsPath)

	connection, err := connectSessionPipe(context.Background(), pipeName)
	if err != nil {
		return fmt.Errorf("connect to service lifecycle pipe: %w", err)
	}
	defer connection.Close()
	return serveSessionHelper(connection, nonce, sessionID, remoteLogs, func(ctx context.Context, ready func() error) error {
		return runAutomaticWithReady(ctx, settings.DebugEnabled(), io.Discard, ready)
	})
}

func serveSessionHelper(connection helperConnection, nonce string, sessionID uint32, remoteLogs *logger.RemoteLogSender, run automaticRunner) error {
	hello, err := sessionipc.NewFrame(sessionipc.TypeHello, 0, 0, sessionipc.HelloMeta{
		Nonce: nonce, PID: os.Getpid(), SessionID: sessionID, Role: sessionHelperRole,
	}, nil)
	if err != nil {
		return err
	}
	if err := sessionipc.WriteFrame(connection, hello); err != nil {
		return fmt.Errorf("send helper hello: %w", err)
	}

	commands := make(chan error, 1)
	conn := sessionipc.NewConn(connection, func(_ context.Context, frame sessionipc.Frame) {
		if frame.Type == sessionipc.TypeStop {
			select {
			case commands <- nil:
			default:
			}
			return
		}
		select {
		case commands <- fmt.Errorf("unexpected service lifecycle frame type %d", frame.Type):
		default:
		}
	})
	if remoteLogs != nil {
		remoteLogs.SetConnection(conn, true)
		defer remoteLogs.ClearConnection(conn)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- conn.Serve(ctx) }()
	runtimeDone := make(chan error, 1)
	go func() {
		runtimeDone <- run(ctx, func() error {
			ready, err := sessionipc.NewFrame(sessionipc.TypeReady, 0, 0, nil, nil)
			if err != nil {
				return err
			}
			return conn.Send(ready)
		})
	}()

	select {
	case runtimeErr := <-runtimeDone:
		_ = conn.Close()
		<-serveDone
		return runtimeErr
	case commandErr := <-commands:
		cancel()
		runtimeErr := <-runtimeDone
		_ = conn.Close()
		<-serveDone
		return errors.Join(commandErr, runtimeErr)
	case serveErr := <-serveDone:
		cancel()
		runtimeErr := <-runtimeDone
		return errors.Join(fmt.Errorf("read service lifecycle frame: %w", serveErr), runtimeErr)
	}
}
