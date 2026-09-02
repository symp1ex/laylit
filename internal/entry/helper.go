package entry

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	helperHelloPrefix = "HELLO "
	helperReady       = "READY"
	helperStop        = "STOP"
	maxProtocolLine   = 512
)

type helperConnection interface {
	io.Reader
	io.Writer
	io.Closer
}

type automaticRunner func(context.Context, func() error) error

func runSessionHelper(pipeName, nonce string) error {
	connection, err := connectSessionPipe(context.Background(), pipeName)
	if err != nil {
		return fmt.Errorf("connect to service lifecycle pipe: %w", err)
	}
	defer connection.Close()
	return serveSessionHelper(connection, nonce, func(ctx context.Context, ready func() error) error {
		return runAutomaticWithReady(ctx, false, io.Discard, ready)
	})
}

func serveSessionHelper(connection helperConnection, nonce string, run automaticRunner) error {
	if err := writeProtocolLine(connection, helperHelloPrefix+nonce); err != nil {
		return fmt.Errorf("send helper hello: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtimeDone := make(chan error, 1)
	go func() {
		runtimeDone <- run(ctx, func() error {
			return writeProtocolLine(connection, helperReady)
		})
	}()
	commandDone := make(chan struct {
		command string
		err     error
	}, 1)
	go func() {
		command, err := readProtocolLine(bufio.NewReaderSize(connection, maxProtocolLine))
		commandDone <- struct {
			command string
			err     error
		}{command: command, err: err}
	}()

	select {
	case err := <-runtimeDone:
		_ = connection.Close()
		<-commandDone
		return err
	case result := <-commandDone:
		cancel()
		runtimeErr := <-runtimeDone
		if result.err != nil {
			return errors.Join(fmt.Errorf("read service lifecycle command: %w", result.err), runtimeErr)
		}
		if result.command != helperStop {
			return errors.Join(fmt.Errorf("unexpected service lifecycle command %q", result.command), runtimeErr)
		}
		return runtimeErr
	}
}

func writeProtocolLine(writer io.Writer, line string) error {
	if strings.ContainsAny(line, "\r\n") || len(line) >= maxProtocolLine {
		return errors.New("invalid lifecycle protocol line")
	}
	_, err := io.WriteString(writer, line+"\n")
	return err
}

func readProtocolLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return "", errors.New("lifecycle protocol line is too long")
	}
	if err != nil {
		return "", err
	}
	if len(line) > maxProtocolLine {
		return "", errors.New("lifecycle protocol line is too long")
	}
	text := string(line)
	return strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r"), nil
}
