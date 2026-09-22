package fixture

import (
	"strings"
	"testing"
)

func TestFilesContentPolicyRequiresExactHTTPSOrigins(t *testing.T) {
	for _, raw := range []string{"https://*", "https://*.example.test", "https://ok.test;img-src *", "http://storage.test", "https://user@storage.test", "https://storage.test/path", "https://storage.test?key=1", "https://storage.test#fragment", "https://a,https://b,https://c,https://d"} {
		if _, err := filesContentPolicy(raw); err == nil {
			t.Errorf("unsafe origin accepted: %q", raw)
		}
	}
	policy, err := filesContentPolicy("https://localhost:18343,https://localhost:18344")
	if err != nil || !strings.Contains(policy, "img-src 'self' blob:") || !strings.Contains(policy, "connect-src 'self' https://localhost:18343 https://localhost:18344;") {
		t.Fatal("exact origins missing", err)
	}
	policy, err = filesContentPolicy("")
	if err != nil || policy != defaultContentPolicy {
		t.Fatal("default policy changed")
	}
}
