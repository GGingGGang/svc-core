package api

import (
	"strings"
	"testing"
)

func TestNormalizeTitle(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
		valid bool
	}{
		{"  회의  ", "회의", true},
		{" \t\n ", "", false},
		{strings.Repeat("한", 255), strings.Repeat("한", 255), true},
		{strings.Repeat("한", 256), strings.Repeat("한", 256), false},
	} {
		got, valid := normalizeTitle(tc.input)
		if got != tc.want || valid != tc.valid {
			t.Errorf("normalizeTitle(%q) = (%q, %v), want (%q, %v)", tc.input, got, valid, tc.want, tc.valid)
		}
	}
}
