package outbox

import (
	"errors"
	"strings"
	"testing"
)

func TestTruncateError(t *testing.T) {
	if got := truncateError(errors.New("temporary failure")); got != "temporary failure" {
		t.Fatalf("short error changed: %q", got)
	}
	long := strings.Repeat("x", 700)
	if got := truncateError(errors.New(long)); len(got) != 512 {
		t.Fatalf("error length = %d, want 512", len(got))
	}
}
