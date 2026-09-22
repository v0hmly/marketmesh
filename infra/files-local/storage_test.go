//go:build ignore

package main

import (
	"slices"
	"testing"
)

func TestBrowserOriginsPreserveRunningAccount(t *testing.T) {
	before, err := browserOrigins(nil, "https://localhost:18443,https://frontdoor:8443")
	if err != nil {
		t.Fatal(err)
	}
	after, err := browserOrigins(before, "https://frontdoor:8443")
	if err != nil || !slices.Equal(before, after) {
		t.Fatal("test overlay replaced persistent origins")
	}
	for _, raw := range []string{"https://*", "https://*.test", "http://localhost:8443", "https://user@host", "https://host/path", "https://host;img-src *", "https://host?x=1"} {
		if _, err := browserOrigins(before, raw); err == nil {
			t.Error("unsafe CORS origin accepted", raw)
		}
	}
	if _, err := browserOrigins([]string{"*"}, ""); err == nil {
		t.Fatal("preexisting wildcard retained")
	}
}
