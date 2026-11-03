package domain

import (
	"errors"
	"testing"
)

func TestValidateScore(t *testing.T) {
	cases := []struct {
		name  string
		score float64
		want  error
	}{
		{"min", 0, nil},
		{"mid", 7.5, nil},
		{"max", 10, nil},
		{"below", -0.1, ErrInvalidScore},
		{"above", 10.1, ErrInvalidScore},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidateScore(tc.score); !errors.Is(got, tc.want) {
				t.Fatalf("ValidateScore(%v) = %v, want %v", tc.score, got, tc.want)
			}
		})
	}
}
