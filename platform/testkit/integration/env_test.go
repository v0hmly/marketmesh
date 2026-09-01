//go:build integration

package integration

import (
	"slices"
	"testing"
)

func TestEnvOrSkipCollectsValuesAndMissingNames(t *testing.T) {
	t.Parallel()

	environment := map[string]string{
		"PRESENT": "value",
		"EMPTY":   "",
	}
	values, missing := requiredEnv(func(name string) (string, bool) {
		value, found := environment[name]
		return value, found
	}, []string{"PRESENT", "EMPTY", "ABSENT", " "})

	if values["PRESENT"] != "value" || len(values) != 1 {
		t.Fatalf("values = %v", values)
	}
	expectedMissing := []string{"EMPTY", "ABSENT", "<empty name>"}
	if !slices.Equal(missing, expectedMissing) {
		t.Fatalf("missing = %v, want %v", missing, expectedMissing)
	}
}
