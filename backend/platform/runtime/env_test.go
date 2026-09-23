package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestEnvTypedValues(t *testing.T) {
	t.Parallel()

	env := MapEnv(map[string]string{
		"REQUIRED": " value ",
		"BOOL":     "true",
		"DURATION": "250ms",
		"INT":      "42",
		"INT64":    "1048576",
		"FLOAT":    "0.25",
	})

	required, err := env.RequiredString("REQUIRED")
	if err != nil || required != "value" {
		t.Fatalf("RequiredString() = %q, %v; want value, nil", required, err)
	}
	boolean, err := env.Bool("BOOL", false)
	if err != nil || !boolean {
		t.Fatalf("Bool() = %v, %v; want true, nil", boolean, err)
	}
	duration, err := env.PositiveDuration("DURATION", time.Second)
	if err != nil || duration != 250*time.Millisecond {
		t.Fatalf("PositiveDuration() = %v, %v; want 250ms, nil", duration, err)
	}
	integer, err := env.PositiveInt("INT", 1)
	if err != nil || integer != 42 {
		t.Fatalf("PositiveInt() = %d, %v; want 42, nil", integer, err)
	}
	integer64, err := env.PositiveInt64("INT64", 1)
	if err != nil || integer64 != 1_048_576 {
		t.Fatalf("PositiveInt64() = %d, %v; want 1048576, nil", integer64, err)
	}
	floating, err := env.Float64("FLOAT", 1)
	if err != nil || floating != 0.25 {
		t.Fatalf("Float64() = %v, %v; want 0.25, nil", floating, err)
	}
}

func TestEnvInvalidValuesDoNotAppearInErrors(t *testing.T) {
	t.Parallel()

	const sensitiveValue = "do-not-leak-this-value"
	tests := []struct {
		name string
		read func(Env) error
	}{
		{
			name: "boolean",
			read: func(env Env) error {
				_, err := env.Bool("VALUE", false)
				return err
			},
		},
		{
			name: "duration",
			read: func(env Env) error {
				_, err := env.Duration("VALUE", time.Second)
				return err
			},
		},
		{
			name: "integer",
			read: func(env Env) error {
				_, err := env.PositiveInt("VALUE", 1)
				return err
			},
		},
		{
			name: "number",
			read: func(env Env) error {
				_, err := env.Float64("VALUE", 1)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := test.read(MapEnv(map[string]string{"VALUE": sensitiveValue}))
			if err == nil {
				t.Fatal("read error = nil, want validation error")
			}
			if strings.Contains(err.Error(), sensitiveValue) {
				t.Fatalf("error %q contains environment value", err)
			}
		})
	}
}

func TestMapEnvCopiesInput(t *testing.T) {
	t.Parallel()

	values := map[string]string{"VALUE": "before"}
	env := MapEnv(values)
	values["VALUE"] = "after"

	value, err := env.RequiredString("VALUE")
	if err != nil {
		t.Fatalf("RequiredString() error = %v", err)
	}
	if value != "before" {
		t.Fatalf("RequiredString() = %q, want before", value)
	}
}

func TestSecretRedactsEveryRepresentation(t *testing.T) {
	t.Parallel()

	const raw = "super-secret-token"
	secret, err := MapEnv(map[string]string{"TOKEN": raw}).Secret("TOKEN", true)
	if err != nil {
		t.Fatalf("Secret() error = %v", err)
	}
	if secret.Reveal() != raw {
		t.Fatal("Reveal() did not return the original secret")
	}
	if !secret.Present() {
		t.Fatal("Present() = false, want true")
	}

	jsonValue, err := json.Marshal(secret)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var logOutput bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logOutput, nil))
	log.Info("secret", "token", secret)

	representations := []string{
		fmt.Sprint(secret),
		fmt.Sprintf("%#v", secret),
		string(jsonValue),
		logOutput.String(),
	}
	for _, representation := range representations {
		if strings.Contains(representation, raw) {
			t.Fatalf("representation %q contains secret", representation)
		}
		if !strings.Contains(representation, RedactedSecretValue) {
			t.Fatalf("representation %q does not contain redaction marker", representation)
		}
	}
}

func TestSecretRequiredErrorDoesNotContainAnotherValue(t *testing.T) {
	t.Parallel()

	env := MapEnv(map[string]string{"OTHER_SECRET": "other-value"})
	_, err := env.Secret("TOKEN", true)
	if err == nil {
		t.Fatal("Secret() error = nil, want required error")
	}
	if strings.Contains(err.Error(), "other-value") {
		t.Fatalf("Secret() error %q contains unrelated secret", err)
	}
}
