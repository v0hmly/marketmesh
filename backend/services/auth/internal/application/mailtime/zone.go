// Package mailtime validates request-scoped display preferences for mail.
package mailtime

import (
	"strings"
	"time"
	_ "time/tzdata" // Auth containers must resolve IANA zones without host tzdata.
)

// Location returns an IANA location or explicit UTC. A display preference is
// untrusted metadata; neither server-local time nor filesystem paths are accepted.
func Location(name string) *time.Location {
	if name == "" || name == "Local" || len(name) > 128 || strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
		return time.UTC
	}
	for _, char := range name {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("/_+-", char)) {
			return time.UTC
		}
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
}
