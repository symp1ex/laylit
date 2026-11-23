package devices

import (
	"context"
	"errors"
	"fmt"

	"laylit/internal/color"
)

var ErrNotFound = errors.New("supported RGB device not found")

type RGBDevice interface {
	SetColor(ctx context.Context, value color.RGB) error
	Off(ctx context.Context) error
	Close() error
}

type CollectionInfo struct {
	Path                string
	VendorID            uint16
	ProductID           uint16
	Interface           int
	UsagePage           uint16
	Usage               uint16
	Serial              string
	Manufacturer        string
	Product             string
	InputReportLength   uint16
	OutputReportLength  uint16
	FeatureReportLength uint16
	Candidate           bool
}

type Inspection struct {
	Provider        string
	Description     string
	NotFoundMessage string
	Collections     []CollectionInfo
	Devices         []Instance
}

// Instance identifies a detected keyboard and opens that particular connection.
// Location changes when the underlying interface path changes.
type Instance struct {
	ID       string
	Name     string
	Location string
	Open     func(context.Context) (RGBDevice, error)
}

// Provider owns detection and opening rules for one device family.
type Provider interface {
	Name() string
	Discover(ctx context.Context) (Inspection, error)
}

type Registry struct {
	providers []Provider
}

func NewRegistry(providers ...Provider) *Registry {
	return &Registry{providers: append([]Provider(nil), providers...)}
}

type Discovery struct {
	Devices     []Instance
	Inspections []Inspection
}

// Discover returns usable devices even if another provider or instance fails.
func (registry *Registry) Discover(ctx context.Context) (Discovery, error) {
	var result Discovery
	var errs []error
	counts := make(map[string]int)
	for _, provider := range registry.providers {
		inspection, err := provider.Discover(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("discover %s devices: %w", provider.Name(), err))
		}
		result.Inspections = append(result.Inspections, inspection)
		for _, device := range inspection.Devices {
			counts[device.ID]++
		}
	}
	for _, inspection := range result.Inspections {
		for _, device := range inspection.Devices {
			if counts[device.ID] != 1 {
				if counts[device.ID] > 1 {
					errs = append(errs, fmt.Errorf("ambiguous RGB device ID %q", device.ID))
					counts[device.ID] = 0
				}
				continue
			}
			result.Devices = append(result.Devices, device)
		}
	}
	return result, errors.Join(errs...)
}
