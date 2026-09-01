package main

import (
	"io"
	"testing"
)

func TestValidateListenAddressRequiresLiteralLoopback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		address   string
		wantError bool
	}{
		{address: "127.0.0.1:18080"},
		{address: "[::1]:18080"},
		{address: "localhost:18080", wantError: true},
		{address: "0.0.0.0:18080", wantError: true},
		{address: "127.0.0.1:80", wantError: true},
	}

	for _, test := range tests {
		t.Run(test.address, func(t *testing.T) {
			t.Parallel()
			err := validateListenAddress(test.address)
			if (err != nil) != test.wantError {
				t.Fatalf("validateListenAddress() error = %v, wantError = %v", err, test.wantError)
			}
		})
	}
}

func TestParseOptionsRequiresBothTargets(t *testing.T) {
	t.Parallel()

	if _, err := parseOptions(nil, io.Discard); err == nil {
		t.Fatal("parseOptions() error = nil, want missing targets error")
	}
	parsed, err := parseOptions([]string{
		"--dc-a-target", "http://127.0.0.1:18081",
		"--dc-b-target", "http://127.0.0.1:18082",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if parsed.dcATarget == parsed.dcBTarget {
		t.Fatal("parsed targets unexpectedly match")
	}
}
