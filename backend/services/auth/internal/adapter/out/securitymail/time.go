package securitymail

import (
	"fmt"
	"strings"
	"time"

	"github.com/v0hmly/marketmesh/services/auth/internal/application/mailtime"
)

func localTimestamp(value time.Time, zone string) string {
	local := value.In(mailtime.Location(zone))
	_, offset := local.Zone()
	suffix := "UTC"
	if offset != 0 {
		sign := "+"
		if offset < 0 {
			sign, offset = "−", -offset
		}
		suffix += fmt.Sprintf("%s%d", sign, offset/3600)
		if minutes := offset % 3600 / 60; minutes != 0 {
			suffix += fmt.Sprintf(":%02d", minutes)
		}
	}
	months := [...]string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}
	return fmt.Sprintf("%d %s %d, %s (%s)", local.Day(), months[local.Month()-1], local.Year(), local.Format("15:04"), suffix)
}

// requestLifetime describes the lifetime measured from this request's creation,
// not SMTP delivery or email opening. Retries never extend it. Reissued codes
// use the remaining challenge lifetime rather than resetting it to ten minutes.
func requestLifetime(start, expiry time.Time) string {
	seconds := int64(expiry.Sub(start) / time.Second)
	if seconds < 1 {
		return "менее секунды с момента запроса"
	}
	var parts []string
	for _, unit := range []struct {
		size  int64
		forms [3]string
	}{
		{3600, [3]string{"час", "часа", "часов"}},
		{60, [3]string{"минуту", "минуты", "минут"}},
		{1, [3]string{"секунду", "секунды", "секунд"}},
	} {
		count := seconds / unit.size
		seconds %= unit.size
		if count == 0 {
			continue
		}
		form := unit.forms[2]
		if count%100 < 11 || count%100 > 14 {
			if count%10 == 1 {
				form = unit.forms[0]
			} else if count%10 >= 2 && count%10 <= 4 {
				form = unit.forms[1]
			}
		}
		parts = append(parts, fmt.Sprintf("%d %s", count, form))
	}
	return strings.Join(parts, " ") + " с момента запроса"
}
