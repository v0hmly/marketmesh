package networkchaos

import (
	"strings"
	"testing"
	"time"
)

func TestValidateQuarantineRequiresOwnerReasonAndExpiry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	valid := Quarantine{
		Scenario:  "dc-a-partition",
		Owner:     "@tunnel-team",
		Reason:    "Upstream runtime issue is tracked separately",
		ExpiresAt: now.Add(7 * 24 * time.Hour),
	}
	if err := ValidateQuarantine(now, valid); err != nil {
		t.Fatalf("ValidateQuarantine() error = %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*Quarantine)
		wantErr string
	}{
		{
			name: "missing owner",
			mutate: func(quarantine *Quarantine) {
				quarantine.Owner = ""
			},
			wantErr: "explicit @owner",
		},
		{
			name: "expired",
			mutate: func(quarantine *Quarantine) {
				quarantine.ExpiresAt = now
			},
			wantErr: "expired",
		},
		{
			name: "unbounded lifetime",
			mutate: func(quarantine *Quarantine) {
				quarantine.ExpiresAt = now.Add(maxQuarantineLifetime + time.Second)
			},
			wantErr: "must not exceed",
		},
		{
			name: "control character in reason",
			mutate: func(quarantine *Quarantine) {
				quarantine.Reason = "line one\nline two"
			},
			wantErr: "control characters",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			quarantine := valid
			test.mutate(&quarantine)
			err := ValidateQuarantine(now, quarantine)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ValidateQuarantine() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestGateAttemptsDoesNotHideFailureWithRetry(t *testing.T) {
	t.Parallel()

	if err := GateAttempts([]bool{true, true}); err != nil {
		t.Fatalf("GateAttempts() error = %v", err)
	}
	for _, attempts := range [][]bool{{false}, {false, true}, {true, false, true}} {
		err := GateAttempts(attempts)
		if err == nil || !strings.Contains(err.Error(), "cannot hide") {
			t.Fatalf("GateAttempts(%v) error = %v, want failure", attempts, err)
		}
	}
	if err := GateAttempts(nil); err == nil || !strings.Contains(err.Error(), "between 1 and") {
		t.Fatalf("GateAttempts(nil) error = %v", err)
	}
}
