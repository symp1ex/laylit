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
}

// Provider owns detection and opening rules for one device family.
type Provider interface {
	Name() string
	Open(ctx context.Context) (RGBDevice, error)
	Inspect(ctx context.Context) (Inspection, error)
}

// Registry tries providers in registration order. Adding a device family only
// requires registering another provider in the composition root.
type Registry struct {
	providers []Provider
}

func NewRegistry(providers ...Provider) *Registry {
	return &Registry{providers: append([]Provider(nil), providers...)}
}

func (registry *Registry) Open(ctx context.Context) (RGBDevice, error) {
	for _, provider := range registry.providers {
		device, err := provider.Open(ctx)
		if err == nil {
			return device, nil
		}
		if errors.Is(err, ErrNotFound) {
			continue
		}
		return nil, fmt.Errorf("open %s device: %w", provider.Name(), err)
	}
	return nil, ErrNotFound
}

func (registry *Registry) Inspect(ctx context.Context) ([]Inspection, error) {
	inspections := make([]Inspection, 0, len(registry.providers))
	for _, provider := range registry.providers {
		inspection, err := provider.Inspect(ctx)
		if err != nil {
			return nil, fmt.Errorf("inspect %s devices: %w", provider.Name(), err)
		}
		inspections = append(inspections, inspection)
	}
	return inspections, nil
}
