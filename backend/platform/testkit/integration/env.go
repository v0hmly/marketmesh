//go:build integration

package integration

import (
	"os"
	"strings"
	"testing"
)

// EnvOrSkip читает обязательные переменные integration-окружения без
// изменения process environment. Если окружение не подготовлено, helper
// пропускает тест, не раскрывая значения переменных в диагностике.
func EnvOrSkip(t testing.TB, names ...string) map[string]string {
	t.Helper()

	values, missing := requiredEnv(os.LookupEnv, names)
	if len(missing) > 0 {
		t.Skipf("testkit: integration environment is not configured: %s", strings.Join(missing, ", "))
	}

	return values
}

func requiredEnv(
	lookup func(string) (string, bool),
	names []string,
) (map[string]string, []string) {
	values := make(map[string]string, len(names))
	missing := make([]string, 0)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			missing = append(missing, "<empty name>")
			continue
		}
		value, found := lookup(name)
		if !found || value == "" {
			missing = append(missing, name)
			continue
		}
		values[name] = value
	}

	return values, missing
}
