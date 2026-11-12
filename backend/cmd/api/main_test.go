package main

import (
	"slices"
	"testing"
)

func TestCORSOrigins(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	cases := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"default", nil, []string{"http://localhost:5173", "http://localhost:4173", "http://127.0.0.1:5173", "http://127.0.0.1:4173"}},
		{"horizon var", map[string]string{"HORIZON_CORS_ORIGINS": " https://web.example/ , ,http://localhost:3000"}, []string{"https://web.example", "http://localhost:3000"}},
		{"legacy var", map[string]string{"CORS_ORIGINS": "https://old.example"}, []string{"https://old.example"}},
		{"horizon wins", map[string]string{"HORIZON_CORS_ORIGINS": "https://new.example", "CORS_ORIGINS": "https://old.example"}, []string{"https://new.example"}},
		{"blank falls back", map[string]string{"HORIZON_CORS_ORIGINS": "  "}, []string{"http://localhost:5173", "http://localhost:4173", "http://127.0.0.1:5173", "http://127.0.0.1:4173"}},
	}
	for _, c := range cases {
		if got := corsOrigins(env(c.env)); !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
