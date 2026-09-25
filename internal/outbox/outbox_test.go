package outbox

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSafePublishFailureNeverRecordsExternalError(t *testing.T) {
	secret := "token=private-key user schedule content " + strings.Repeat("x", 600)
	for _, test := range []struct {
		err  error
		want string
	}{
		{errors.New(secret), "publish_failed"},
		{context.DeadlineExceeded, "publish_timeout"},
	} {
		if got := safePublishFailure(test.err); got != test.want || strings.Contains(got, secret) {
			t.Fatalf("unsafe publish failure code: %q", got)
		}
	}
}
