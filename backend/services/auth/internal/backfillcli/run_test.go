package backfillcli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRejectsArgumentsBeforeReadingSecrets(t *testing.T) {
	for _, args := range [][]string{{"--limit=0"}, {"--limit=1001"}, {"--after=invalid"}, {"--dsn=secret"}, {"extra"}} {
		err := Run(context.Background(), args, func(string) string { t.Fatal("read environment before validation"); return "" }, &bytes.Buffer{})
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal(err)
		}
	}
}
func TestSafeConfigurationErrorsAndCanceled(t *testing.T) {
	for _, dsn := range []string{"", "://password secret"} {
		err := Run(context.Background(), nil, func(key string) string {
			if key != "MARKETMESH_AUTH_POSTGRES_DSN" {
				t.Fatal("foreign configuration")
			}
			return dsn
		}, &bytes.Buffer{})
		if err == nil || strings.Contains(err.Error(), "password") {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, nil, func(string) string { t.Fatal("read env after cancellation"); return "" }, &bytes.Buffer{}); err == nil {
		t.Fatal("canceled")
	}
}
