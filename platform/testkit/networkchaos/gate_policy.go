package networkchaos

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxQuarantineReasonRunes = 256
	maxQuarantineLifetime    = 30 * 24 * time.Hour
)

var quarantineOwnerPattern = regexp.MustCompile(`^@[A-Za-z0-9][A-Za-z0-9._/-]{1,62}$`)

// Quarantine — явная ограниченная по времени метаинформация для отдельного
// non-required scenario. Она не превращает failed attempt в успешный gate.
type Quarantine struct {
	Scenario  string
	Owner     string
	Reason    string
	ExpiresAt time.Time
}

// ValidateQuarantine отклоняет анонимную, бессрочную и слишком долгую
// quarantine. now передаётся явно для детерминированных проверок.
func ValidateQuarantine(now time.Time, quarantine Quarantine) error {
	if now.IsZero() {
		return errors.New("networkchaos: quarantine validation time must not be zero")
	}
	if !scenarioNamePattern.MatchString(quarantine.Scenario) {
		return errors.New("networkchaos: quarantine scenario must be a bounded lowercase slug")
	}
	if !quarantineOwnerPattern.MatchString(quarantine.Owner) {
		return errors.New("networkchaos: quarantine owner must be an explicit @owner")
	}
	reasonLength := utf8.RuneCountInString(quarantine.Reason)
	if reasonLength == 0 || reasonLength > maxQuarantineReasonRunes {
		return fmt.Errorf(
			"networkchaos: quarantine reason must contain between 1 and %d characters",
			maxQuarantineReasonRunes,
		)
	}
	if strings.IndexFunc(quarantine.Reason, unicode.IsControl) >= 0 {
		return errors.New("networkchaos: quarantine reason must not contain control characters")
	}
	if !quarantine.ExpiresAt.After(now) {
		return errors.New("networkchaos: quarantine is expired")
	}
	if quarantine.ExpiresAt.Sub(now) > maxQuarantineLifetime {
		return fmt.Errorf(
			"networkchaos: quarantine lifetime must not exceed %s",
			maxQuarantineLifetime,
		)
	}

	return nil
}
