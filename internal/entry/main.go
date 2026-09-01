package entry

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"evision-rgb/internal/app"
	"evision-rgb/internal/color"
	"evision-rgb/internal/config"
	"evision-rgb/internal/devices"
	"evision-rgb/internal/devices/evision"
	"evision-rgb/internal/hid"
	windowslayouts "evision-rgb/internal/layouts/windows"
	"evision-rgb/internal/winconsole"
)

func Main(args []string, attachForCommands bool) int {
	stdout, stderr, closeOutputs := winconsole.Outputs(attachForCommands && len(args) > 0)
	defer closeOutputs()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := Run(ctx, args, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("evision-rgb", flag.ContinueOnError)
	flags.SetOutput(stderr)
	debug := flags.Bool("debug", false, "show HID discovery, layout, and report diagnostics")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage:")
		fmt.Fprintln(stderr, "  evision-rgb [--debug]")
		fmt.Fprintln(stderr, "  evision-rgb [--debug] info")
		fmt.Fprintln(stderr, "  evision-rgb [--debug] set <RRGGBB|#RRGGBB>")
		fmt.Fprintln(stderr, "  evision-rgb [--debug] off")
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	commandArgs := flags.Args()
	if len(commandArgs) == 0 {
		return runAutomatic(ctx, *debug, stderr)
	}

	transport := hid.NewWindowsTransport()
	provider := evision.NewProvider(transport, evision.Options{Debug: *debug, DebugWriter: stderr})
	registry := devices.NewRegistry(provider)
	switch commandArgs[0] {
	case "info":
		if len(commandArgs) != 1 {
			return errors.New("info does not accept arguments")
		}
		return printInfo(ctx, stdout, registry)
	case "set":
		if len(commandArgs) != 2 {
			return errors.New("set requires one color argument")
		}
		value, err := color.Parse(commandArgs[1])
		if err != nil {
			return err
		}
		device, err := registry.Open(ctx)
		if err != nil {
			return err
		}
		defer device.Close()
		if err := device.SetColor(ctx, value); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Color set to %s\n", value)
		return nil
	case "off":
		if len(commandArgs) != 1 {
			return errors.New("off does not accept arguments")
		}
		device, err := registry.Open(ctx)
		if err != nil {
			return err
		}
		defer device.Close()
		if err := device.Off(ctx); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Lighting off")
		return nil
	default:
		flags.Usage()
		return fmt.Errorf("unknown command %q", commandArgs[0])
	}
}

func runAutomatic(ctx context.Context, debug bool, fallbackWriter io.Writer) error {
	configPath, err := applicationConfigPath()
	if err != nil {
		return err
	}
	logFile, err := openLogFile()
	if err != nil {
		return err
	}
	defer logFile.Close()
	logWriter := io.Writer(logFile)
	if debug {
		logWriter = io.MultiWriter(logFile, fallbackWriter)
	}
	logger := log.New(logWriter, "", log.Ldate|log.Ltime|log.Lmicroseconds)
	logger.Printf("starting automatic mode; config=%s", configPath)
	var debugf func(string, ...any)
	debugWriter := logWriter
	if debug {
		debugf = func(format string, args ...any) { logger.Printf("DEBUG "+format, args...) }
		debugWriter = timestampedDebugWriter{logger: logger}
	}

	transport := hid.NewWindowsTransport()
	provider := evision.NewProvider(transport, evision.Options{Debug: debug, DebugWriter: debugWriter})
	registry := devices.NewRegistry(provider)
	runtime := app.Runtime{
		Layouts: windowslayouts.NewSourceWithDebug(debugf), Config: config.NewFileRepository(configPath), Devices: registry,
		ReportError: func(err error) { logger.Printf("runtime warning: %v", err) },
		Tracef:      debugf,
	}
	if err := runtime.Run(ctx); err != nil {
		logger.Printf("automatic mode stopped: %v", err)
		return err
	}
	logger.Print("automatic mode stopped")
	return nil
}

type timestampedDebugWriter struct {
	logger *log.Logger
}

func (writer timestampedDebugWriter) Write(data []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line != "" {
			writer.logger.Print(line)
		}
	}
	return len(data), nil
}

func applicationConfigPath() (string, error) {
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("determine user config directory: %w", err)
	}
	return filepath.Join(directory, "evision-rgb", "config.json"), nil
}

func openLogFile() (*os.File, error) {
	directory, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("determine user log directory: %w", err)
	}
	directory = filepath.Join(directory, "evision-rgb")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("create log directory %q: %w", directory, err)
	}
	path := filepath.Join(directory, "evision-rgb.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file %q: %w", path, err)
	}
	return file, nil
}

func printInfo(ctx context.Context, writer io.Writer, registry *devices.Registry) error {
	inspections, err := registry.Inspect(ctx)
	if err != nil {
		return err
	}
	found := 0
	for _, inspection := range inspections {
		fmt.Fprintf(writer, "Found %d %s\n", len(inspection.Collections), inspection.Description)
		found += len(inspection.Collections)
		for index, info := range inspection.Collections {
			fmt.Fprintf(writer, "\n[%d]\n", index)
			fmt.Fprintf(writer, "Device: %s\n", textOrNA(info.Product))
			fmt.Fprintf(writer, "VID: %04X\n", info.VendorID)
			fmt.Fprintf(writer, "PID: %04X\n", info.ProductID)
			fmt.Fprintf(writer, "Path: %s\n", textOrNA(info.Path))
			fmt.Fprintf(writer, "Interface: %s\n", interfaceOrNA(info.Interface))
			fmt.Fprintf(writer, "Usage Page: %04X\n", info.UsagePage)
			fmt.Fprintf(writer, "Usage: %04X\n", info.Usage)
			fmt.Fprintf(writer, "Serial: %s\n", textOrNA(info.Serial))
			fmt.Fprintf(writer, "Manufacturer: %s\n", textOrNA(info.Manufacturer))
			fmt.Fprintf(writer, "Product: %s\n", textOrNA(info.Product))
			fmt.Fprintf(writer, "Input Report: %d bytes\n", info.InputReportLength)
			fmt.Fprintf(writer, "Output Report: %d bytes\n", info.OutputReportLength)
			fmt.Fprintf(writer, "Feature Report: %d bytes\n", info.FeatureReportLength)
			fmt.Fprintf(writer, "RGB candidate: %s\n", boolText(info.Candidate))
		}
	}
	if found == 0 {
		if len(inspections) > 0 && inspections[0].NotFoundMessage != "" {
			return errors.New(inspections[0].NotFoundMessage)
		}
		return devices.ErrNotFound
	}
	return nil
}

func textOrNA(value string) string {
	if value == "" {
		return "N/A"
	}
	return value
}

func interfaceOrNA(value int) string {
	if value < 0 {
		return "N/A"
	}
	return fmt.Sprintf("%d", value)
}

func boolText(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
