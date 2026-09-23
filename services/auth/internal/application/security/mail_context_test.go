package security

import (
	"context"
	"errors"
	"testing"
)

func TestMailPreferencePreservesRequestCancellationAndDoesNotLeak(t *testing.T) {
	parent, cancel := context.WithCancel(t.Context())
	moscow := WithMailTimeZone(parent, "Europe/Moscow")
	if mailTimeZone(moscow) != "Europe/Moscow" || mailTimeZone(parent) != "UTC" {
		t.Fatal("request preference leaked to parent")
	}
	for _, name := range []string{"", "Local", "not-a-zone", "Europe/Moscow,UTC"} {
		if mailTimeZone(WithMailTimeZone(moscow, name)) != "UTC" {
			t.Fatal("invalid preference inherited another request's zone")
		}
	}
	cancel()
	if !errors.Is(moscow.Err(), context.Canceled) {
		t.Fatal("preference dropped cancellation")
	}
}
