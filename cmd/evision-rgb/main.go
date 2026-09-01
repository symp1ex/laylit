package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"evision-rgb/internal/color"
	"evision-rgb/internal/keyboard"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("evision-rgb", flag.ContinueOnError)
	flags.SetOutput(stderr)
	debug := flags.Bool("debug", false, "show HID discovery and report diagnostics")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage:")
		fmt.Fprintln(stderr, "  evision-rgb [--debug] info")
		fmt.Fprintln(stderr, "  evision-rgb [--debug] set <RRGGBB|#RRGGBB>")
		fmt.Fprintln(stderr, "  evision-rgb [--debug] off")
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	commandArgs := flags.Args()
	if len(commandArgs) == 0 {
		flags.Usage()
		return errors.New("command is required")
	}

	options := keyboard.Options{Debug: *debug, DebugWriter: stderr}
	switch commandArgs[0] {
	case "info":
		if len(commandArgs) != 1 {
			return errors.New("info does not accept arguments")
		}
		return printInfo(stdout, options)
	case "set":
		if len(commandArgs) != 2 {
			return errors.New("set requires one color argument")
		}
		rgb, err := color.Parse(commandArgs[1])
		if err != nil {
			return err
		}
		device, err := keyboard.Open(options)
		if err != nil {
			return err
		}
		defer device.Close()
		if err := device.SetColor(rgb.R, rgb.G, rgb.B); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Color set to #%02X%02X%02X\n", rgb.R, rgb.G, rgb.B)
		return nil
	case "off":
		if len(commandArgs) != 1 {
			return errors.New("off does not accept arguments")
		}
		device, err := keyboard.Open(options)
		if err != nil {
			return err
		}
		defer device.Close()
		if err := device.Off(); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Lighting off")
		return nil
	default:
		flags.Usage()
		return fmt.Errorf("unknown command %q", commandArgs[0])
	}
}

func printInfo(writer io.Writer, options keyboard.Options) error {
	infos, err := keyboard.Enumerate(options)
	if err != nil {
		return err
	}
	fmt.Fprintf(writer, "Found %d HID interfaces for 320F:5000\n", len(infos))
	for index, info := range infos {
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
		fmt.Fprintf(writer, "RGB candidate: %s\n", boolText(info.RGBCandidate()))
	}
	if len(infos) == 0 {
		return errors.New("EVision keyboard 320F:5000 not found")
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
