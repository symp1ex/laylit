package entry

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCLIArgumentValidationBeforeHardwareAccess(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"info", "extra"}, "info does not accept arguments"},
		{[]string{"set"}, "set requires one color argument"},
		{[]string{"set", "not-a-color"}, "invalid color"},
		{[]string{"off", "extra"}, "off does not accept arguments"},
		{[]string{"unknown"}, "unknown command"},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		err := Run(context.Background(), test.args, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("Run(%v) error = %v, want substring %q", test.args, err, test.want)
		}
	}
}
