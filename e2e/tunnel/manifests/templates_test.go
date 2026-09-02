package manifests

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderProducesBoundedOwnedWorkloads(t *testing.T) {
	t.Parallel()

	parameters := validParameters()
	dmz, err := RenderDMZ(parameters)
	if err != nil {
		t.Fatalf("RenderDMZ() error = %v", err)
	}
	internal, err := RenderInternal(parameters)
	if err != nil {
		t.Fatalf("RenderInternal() error = %v", err)
	}
	combined := append(append([]byte(nil), dmz...), internal...)

	for _, required := range []string{
		"name: mm29-gateway-in",
		"name: mm29-gateway-out",
		"name: mm29-fake-internal",
		"namespace: marketmesh-e2e-tunnel",
		"marketmesh.io/run-id: run-29",
		"replicas: 2",
		"maxUnavailable: 0",
		"maxSurge: 1",
		"initialDelaySeconds: 10",
		"progressDeadlineSeconds: 120",
		"terminationGracePeriodSeconds: 30",
		"readOnlyRootFilesystem: true",
		"runAsUser: 65532",
		"fsGroup: 65532",
		"defaultMode: 0440",
		"command: [/gateway-in, prestop]",
		"command: [/gateway-out, prestop]",
		"command: [/fake-internal, prestop]",
		"name: DATA_CENTER",
		"name: E2E_ROUTING_SNAPSHOT_ENABLED",
		"name: MAX_LEDGER_ENTRIES",
		"value: \"50000\"",
		"value: dc-a",
		"value: passthrough:///dc-a-dmz-control-plane:30443",
		"port: 30443",
	} {
		if !bytes.Contains(combined, []byte(required)) {
			t.Errorf("rendered manifests do not contain %q", required)
		}
	}
	if bytes.Contains(combined, []byte("{{")) {
		t.Fatal("rendered manifests retain a template marker")
	}
	if count := bytes.Count(combined, []byte("imagePullPolicy: Never")); count != 3 {
		t.Fatalf("local-only image pull policies = %d, want 3", count)
	}
}

func TestRenderRejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Parameters)
	}{
		{name: "unsafe run id", mutate: func(p *Parameters) { p.RunID = "run/29" }},
		{name: "unknown dc", mutate: func(p *Parameters) { p.DC = "dc-c" }},
		{name: "yaml image injection", mutate: func(p *Parameters) {
			p.GatewayOutImage = "image\nkind: Secret"
		}},
		{name: "arbitrary tunnel port", mutate: func(p *Parameters) {
			p.GatewayInTarget = "127.0.0.1:22"
		}},
		{name: "foreign identity", mutate: func(p *Parameters) {
			p.GatewayOutURI = strings.Replace(p.GatewayOutURI, "marketmesh.test", "foreign.test", 1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			parameters := validParameters()
			test.mutate(&parameters)
			if _, err := RenderInternal(parameters); err == nil {
				t.Fatal("RenderInternal() error = nil, want validation error")
			}
		})
	}
}

func validParameters() Parameters {
	return Parameters{
		RunID: "run-29", DC: "dc-a", Version: "3e264fb",
		GatewayInImage:    "marketmesh/gateway-in:mm29",
		GatewayOutImage:   "marketmesh/gateway-out:mm29",
		FakeInternalImage: "marketmesh/fake-internal:mm29",
		GatewayInTarget:   "passthrough:///dc-a-dmz-control-plane:30443",
		GatewayInURI:      "spiffe://marketmesh.test/e2e/run-29/dc-a/gateway-in",
		GatewayOutURI:     "spiffe://marketmesh.test/e2e/run-29/dc-a/gateway-out",
		FakeInternalURI:   "spiffe://marketmesh.test/e2e/run-29/dc-a/fake-internal",
	}
}
