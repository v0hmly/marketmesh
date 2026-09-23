package workloadid

import "testing"

func TestScopedIdentityBoundaries(t *testing.T) {
	want := Scope{TrustDomain: "marketmesh.test", Environment: "dev", Cluster: "dc-a", Namespace: "marketmesh", ServiceAccount: "files"}
	for _, suffix := range []string{"", "/pod/01234567-89ab-cdef-0123-456789abcdef"} {
		got, pod, err := ParseScopedURI(want.String() + suffix)
		if err != nil || got != want || (suffix != "" && pod == "") {
			t.Fatal("valid identity rejected")
		}
	}
	for _, raw := range []string{want.String() + "?scope=admin", want.String() + "#admin", want.String() + "/pod/not-a-uid", "spiffe://marketmesh.test/dev/files", "spiffe://marketmesh.test/env/dev/cluster/dc-a/ns/marketmesh/sa/%66iles", want.String() + "/extra"} {
		if _, _, err := ParseScopedURI(raw); err == nil {
			t.Fatal("ambiguous scope accepted")
		}
	}
	for _, raw := range []string{"spiffe://marketmesh.test/env/prod/cluster/dc-a/ns/marketmesh/sa/files", "spiffe://marketmesh.test/env/dev/cluster/dc-b/ns/marketmesh/sa/files", "spiffe://marketmesh.test/env/dev/cluster/dc-a/ns/foreign/sa/files", "spiffe://marketmesh.test/env/dev/cluster/dc-a/ns/marketmesh/sa/other"} {
		got, _, err := ParseScopedURI(raw)
		if err != nil || got == want {
			t.Fatal("scope component ignored")
		}
	}
}
