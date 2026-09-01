package color

import (
	"errors"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		input string
		want  RGB
	}{
		{"#FF0000", RGB{R: 0xFF}},
		{"FF0000", RGB{R: 0xFF}},
		{"ff0000", RGB{R: 0xFF}},
		{"00FF7F", RGB{G: 0xFF, B: 0x7F}},
		{"000000", RGB{}},
		{"FFFFFF", RGB{R: 0xFF, G: 0xFF, B: 0xFF}},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, err := Parse(test.input)
			if err != nil {
				t.Fatalf("Parse(%q): %v", test.input, err)
			}
			if got != test.want {
				t.Fatalf("Parse(%q) = %#v, want %#v", test.input, got, test.want)
			}
		})
	}
}

func TestParseInvalid(t *testing.T) {
	for _, input := range []string{"#FFF", "GG0000", "1234567", ""} {
		t.Run(input, func(t *testing.T) {
			_, err := Parse(input)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Parse(%q) error = %v, want %v", input, err, ErrInvalid)
			}
		})
	}
}
