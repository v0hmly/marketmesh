package e2esnapshot

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestSnapshotRoundTrip(t *testing.T) {
	t.Parallel()

	want := validSnapshot()
	var encoded bytes.Buffer
	if err := Encode(&encoded, want); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	got, err := Decode(&encoded)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got.SchemaVersion != want.SchemaVersion ||
		got.GatewayInInstance != want.GatewayInInstance ||
		len(got.Routes) != len(want.Routes) {
		t.Fatalf("Decode() = %+v", got)
	}
}

func TestDecodeRejectsAmbiguousDocuments(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		data string
	}{
		{name: "empty", data: ""},
		{name: "duplicate field", data: `{"schema_version":"a","schema_version":"b"}`},
		{name: "unknown field", data: `{"schema_version":"a","unknown":true}`},
		{name: "trailing document", data: `{}` + `{}`},
		{name: "oversized", data: strings.Repeat(" ", maxDocumentBytes+1)},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := Decode(strings.NewReader(testCase.data))
			if !errors.Is(err, ErrInvalidDocument) {
				t.Fatalf("Decode() error = %v, want ErrInvalidDocument", err)
			}
		})
	}
}

func TestValidateRejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{name: "schema", mutate: func(snapshot *Snapshot) { snapshot.SchemaVersion = "v2" }},
		{name: "instance", mutate: func(snapshot *Snapshot) { snapshot.GatewayInInstance = "--all" }},
		{name: "route order", mutate: func(snapshot *Snapshot) {
			snapshot.Routes[0], snapshot.Routes[1] = snapshot.Routes[1], snapshot.Routes[0]
		}},
		{name: "route disabled", mutate: func(snapshot *Snapshot) { snapshot.Routes[0].RouteAllowed = false }},
		{name: "draining", mutate: func(snapshot *Snapshot) { snapshot.Routes[0].RegistryDraining = true }},
		{name: "empty tunnels", mutate: func(snapshot *Snapshot) { snapshot.Routes[0].Tunnels = nil }},
		{name: "uppercase id", mutate: func(snapshot *Snapshot) {
			snapshot.Routes[0].Tunnels[0].InstanceID = strings.Repeat("A", 32)
		}},
		{name: "foreign dc", mutate: func(snapshot *Snapshot) {
			snapshot.Routes[0].Tunnels[0].DataCenter = "dc-c"
		}},
		{name: "unknown state", mutate: func(snapshot *Snapshot) {
			snapshot.Routes[0].Tunnels[0].State = "unknown"
		}},
		{name: "negative active", mutate: func(snapshot *Snapshot) {
			snapshot.Routes[0].Tunnels[0].ActiveRequests = -1
		}},
		{name: "unsorted tunnels", mutate: func(snapshot *Snapshot) {
			snapshot.Routes[0].Tunnels = []TunnelSnapshot{
				validTunnel("dc-b", 'b'),
				validTunnel("dc-a", 'a'),
			}
		}},
		{name: "duplicate tunnel", mutate: func(snapshot *Snapshot) {
			snapshot.Routes[0].Tunnels = []TunnelSnapshot{
				validTunnel("dc-a", 'a'),
				validTunnel("dc-a", 'a'),
			}
		}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			snapshot := validSnapshot()
			testCase.mutate(&snapshot)
			if err := snapshot.Validate(); !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("Validate() error = %v, want ErrInvalidSnapshot", err)
			}
		})
	}
}

func FuzzDecodeNeverAcceptsAnInvalidSnapshot(f *testing.F) {
	var valid bytes.Buffer
	if err := Encode(&valid, validSnapshot()); err != nil {
		f.Fatalf("Encode() seed error = %v", err)
	}
	f.Add(valid.String())
	f.Add(`{"schema_version":"a","schema_version":"b"}`)
	f.Add(`[]`)

	f.Fuzz(func(t *testing.T, document string) {
		snapshot, err := Decode(strings.NewReader(document))
		if err != nil {
			return
		}
		if err := snapshot.Validate(); err != nil {
			t.Fatalf("Decode() accepted invalid snapshot: %v", err)
		}
	})
}

func validSnapshot() Snapshot {
	routes := []Route{RouteUserGetMe, RouteUserUpdateMe}
	snapshots := make([]RouteSnapshot, 0, len(routes))
	for _, route := range routes {
		snapshots = append(snapshots, RouteSnapshot{
			Route:        route,
			RouteAllowed: true,
			Tunnels: []TunnelSnapshot{
				validTunnel("dc-a", 'a'),
				validTunnel("dc-b", 'b'),
			},
		})
	}
	return Snapshot{
		SchemaVersion:     SchemaVersion,
		GatewayInInstance: "mm29-gateway-in-abcde",
		Routes:            snapshots,
	}
}

func validTunnel(dataCenter string, character byte) TunnelSnapshot {
	return TunnelSnapshot{
		InstanceID: strings.Repeat(string(character), 32),
		DataCenter: dataCenter,
		State:      TunnelStateReady,
	}
}
