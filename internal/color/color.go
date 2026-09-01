package color

import (
	"errors"
	"strconv"
)

var ErrInvalid = errors.New("invalid color: expected RRGGBB or #RRGGBB")

type RGB struct {
	R byte
	G byte
	B byte
}

func Parse(value string) (RGB, error) {
	if len(value) == 7 && value[0] == '#' {
		value = value[1:]
	}
	if len(value) != 6 {
		return RGB{}, ErrInvalid
	}

	parsed, err := strconv.ParseUint(value, 16, 24)
	if err != nil {
		return RGB{}, ErrInvalid
	}

	return RGB{
		R: byte(parsed >> 16),
		G: byte(parsed >> 8),
		B: byte(parsed),
	}, nil
}
