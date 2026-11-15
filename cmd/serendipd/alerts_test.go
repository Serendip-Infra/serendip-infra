package main

import "testing"

func TestOneSecretString(t *testing.T) {
	for _, tt := range []struct {
		name string
		data map[string]any
		want string
		ok   bool
	}{
		{"value key", map[string]any{"value": "tok", "other": "x"}, "tok", true},
		{"only key", map[string]any{"token": "tok"}, "tok", true},
		{"two keys", map[string]any{"a": "1", "b": "2"}, "", false},
		{"not a string", map[string]any{"value": 3}, "", false},
		{"empty", map[string]any{}, "", false},
	} {
		got, err := oneSecretString(tt.data)
		if got != tt.want || (err == nil) != tt.ok {
			t.Errorf("%s: %q, %v", tt.name, got, err)
		}
	}
}
