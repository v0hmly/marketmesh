package main

import (
	"testing"
)

func TestValidateAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		address   string
		wantError bool
	}{
		{name: "valid", address: "172.28.10.2:18443"},
		{name: "hostname", address: "example.com:18443", wantError: true},
		{name: "ipv6", address: "[::1]:18443", wantError: true},
		{name: "privileged port", address: "172.28.10.2:443", wantError: true},
		{name: "missing port", address: "172.28.10.2", wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateAddress(test.address)
			if (err != nil) != test.wantError {
				t.Errorf("validateAddress(%q) error = %v, wantError %v", test.address, err, test.wantError)
			}
		})
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	t.Parallel()

	if err := run([]string{"unknown"}); err == nil {
		t.Fatal("run(unknown) error = nil, want error")
	}
}
