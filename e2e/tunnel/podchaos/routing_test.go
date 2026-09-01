package podchaos

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestDecodeRoutingSnapshotIsStrictAndBounded(t *testing.T) {
	t.Parallel()

	valid := routingSnapshotForGateway("mm29-gateway-in-a", "dc-a", []RoutingTunnelSnapshot{{
		InstanceID: "11111111111111111111111111111111",
		DataCenter: "dc-a", State: "ready",
	}})
	document, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	decoded, err := DecodeRoutingSnapshot(bytes.NewReader(document))
	if err != nil {
		t.Fatalf("DecodeRoutingSnapshot() error = %v", err)
	}
	if decoded.GatewayInInstance != valid.GatewayInInstance {
		t.Fatalf("gateway instance = %q", decoded.GatewayInInstance)
	}

	tests := []struct {
		name     string
		document []byte
	}{
		{name: "empty", document: nil},
		{name: "duplicate field", document: []byte(`{
			"schema_version":"marketmesh.gateway-in.e2e.routing-snapshot/v1",
			"schema_version":"marketmesh.gateway-in.e2e.routing-snapshot/v1"
		}`)},
		{name: "unknown field", document: append(document[:len(document)-1], []byte(`,"unknown":true}`)...)},
		{name: "trailing document", document: append(slices.Clone(document), []byte(`{}`)...)},
		{name: "oversized", document: bytes.Repeat([]byte{'x'}, maxRoutingDocument+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := DecodeRoutingSnapshot(bytes.NewReader(test.document)); !errors.Is(err, ErrUnsafeState) {
				t.Fatalf("DecodeRoutingSnapshot() error = %v", err)
			}
		})
	}
}

func FuzzDecodeRoutingSnapshotNeverAcceptsInvalidState(f *testing.F) {
	valid := routingSnapshotForGateway(
		"mm29-gateway-in-a",
		"dc-a",
		[]RoutingTunnelSnapshot{{
			InstanceID: "11111111111111111111111111111111",
			DataCenter: "dc-a", State: "ready",
		}},
	)
	document, err := json.Marshal(valid)
	if err != nil {
		f.Fatalf("json.Marshal() error = %v", err)
	}
	f.Add(document)
	f.Add([]byte(`{"schema_version":"a","schema_version":"b"}`))
	f.Add([]byte(`[]`))

	f.Fuzz(func(t *testing.T, document []byte) {
		snapshot, err := DecodeRoutingSnapshot(bytes.NewReader(document))
		if err != nil {
			return
		}
		dc, err := routingSnapshotDC(snapshot)
		if err != nil {
			t.Fatalf("accepted snapshot has no valid data center: %v", err)
		}
		if err := validateRoutingSnapshot(dc, snapshot); err != nil {
			t.Fatalf("accepted snapshot is invalid: %v", err)
		}
	})
}

func TestResolveRoleSelectsExactGatewayInPod(t *testing.T) {
	t.Parallel()

	pods := []PodRef{
		routingPod("mm29-gateway-in-a", ComponentGatewayIn),
		routingPod("mm29-gateway-in-b", ComponentGatewayIn),
	}
	snapshots := []RoutingSnapshot{
		routingSnapshotForGateway(pods[0].Name, "dc-a", []RoutingTunnelSnapshot{{
			InstanceID: "11111111111111111111111111111111",
			DataCenter: "dc-a",
			State:      "ready", ActiveRequests: 1,
		}}),
		routingSnapshotForGateway(pods[1].Name, "dc-a", []RoutingTunnelSnapshot{{
			InstanceID: "22222222222222222222222222222222",
			DataCenter: "dc-a",
			State:      "ready",
		}}),
	}

	selected, healthy, retained, err := resolveRole(Step{
		DC: DCA, Component: ComponentGatewayIn, Role: RoleActive,
	}, pods, snapshots)
	if err != nil {
		t.Fatalf("resolveRole(active) error = %v", err)
	}
	if selected != pods[0] || healthy != 2 || retained != 1 {
		t.Fatalf("resolveRole(active) = (%+v, %d, %d)", selected, healthy, retained)
	}

	selected, _, _, err = resolveRole(Step{
		DC: DCA, Component: ComponentGatewayIn, Role: RoleStandby,
	}, pods, snapshots)
	if err != nil {
		t.Fatalf("resolveRole(standby) error = %v", err)
	}
	if selected != pods[1] {
		t.Fatalf("resolveRole(standby) pod = %+v", selected)
	}
}

func TestResolveRoleSelectsExactGatewayOutPodFromBothSlots(t *testing.T) {
	t.Parallel()

	pods := []PodRef{
		routingPod("mm29-gateway-out-a", ComponentGatewayOut),
		routingPod("mm29-gateway-out-b", ComponentGatewayOut),
	}
	snapshots := gatewayOutRoutingSnapshots(t, pods)
	for routeIndex := range snapshots[0].Routes {
		for tunnelIndex := range snapshots[0].Routes[routeIndex].Tunnels {
			if snapshots[0].Routes[routeIndex].Tunnels[tunnelIndex].InstanceID ==
				mustGatewayOutIDs(t, pods[0].Name)[0] {
				snapshots[0].Routes[routeIndex].Tunnels[tunnelIndex].ActiveRequests = 1
			}
		}
	}

	selected, healthy, retained, err := resolveRole(Step{
		DC: DCA, Component: ComponentGatewayOut, Role: RoleActive,
	}, pods, snapshots)
	if err != nil {
		t.Fatalf("resolveRole(active) error = %v", err)
	}
	if selected != pods[0] || healthy != 2 || retained != 1 {
		t.Fatalf("resolveRole(active) = (%+v, %d, %d)", selected, healthy, retained)
	}

	selected, _, _, err = resolveRole(Step{
		DC: DCA, Component: ComponentGatewayOut, Role: RoleStandby,
	}, pods, snapshots)
	if err != nil {
		t.Fatalf("resolveRole(standby) error = %v", err)
	}
	if selected != pods[1] {
		t.Fatalf("resolveRole(standby) pod = %+v", selected)
	}
}

func TestResolveRoleBreaksActivityTiesDeterministicallyAndRejectsForeignInstances(t *testing.T) {
	t.Parallel()

	pods := []PodRef{
		routingPod("mm29-gateway-in-a", ComponentGatewayIn),
		routingPod("mm29-gateway-in-b", ComponentGatewayIn),
	}
	snapshots := []RoutingSnapshot{
		routingSnapshotForGateway(pods[0].Name, "dc-a", []RoutingTunnelSnapshot{{
			InstanceID: "11111111111111111111111111111111", DataCenter: "dc-a",
			State: "ready", ActiveRequests: 1,
		}}),
		routingSnapshotForGateway(pods[1].Name, "dc-a", []RoutingTunnelSnapshot{{
			InstanceID: "22222222222222222222222222222222", DataCenter: "dc-a",
			State: "ready", ActiveRequests: 1,
		}}),
	}
	selected, _, _, err := resolveRole(Step{
		DC: DCA, Component: ComponentGatewayIn, Role: RoleActive,
	}, pods, snapshots)
	if err != nil || selected != pods[0] {
		t.Fatalf("tied active resolveRole() = (%+v, %v), want first pod", selected, err)
	}
	selected, _, _, err = resolveRole(Step{
		DC: DCA, Component: ComponentGatewayIn, Role: RoleStandby,
	}, pods, snapshots)
	if err != nil || selected != pods[1] {
		t.Fatalf("tied standby resolveRole() = (%+v, %v), want last pod", selected, err)
	}

	outPods := []PodRef{
		routingPod("mm29-gateway-out-a", ComponentGatewayOut),
		routingPod("mm29-gateway-out-b", ComponentGatewayOut),
	}
	outSnapshots := gatewayOutRoutingSnapshots(t, outPods)
	foreign := RoutingTunnelSnapshot{
		InstanceID: "ffffffffffffffffffffffffffffffff",
		DataCenter: "dc-a", State: "ready",
	}
	for index := range outSnapshots {
		for routeIndex := range outSnapshots[index].Routes {
			outSnapshots[index].Routes[routeIndex].Tunnels = append(
				outSnapshots[index].Routes[routeIndex].Tunnels,
				foreign,
			)
			sortRoutingTunnels(outSnapshots[index].Routes[routeIndex].Tunnels)
		}
	}
	_, _, _, err = resolveRole(Step{
		DC: DCA, Component: ComponentGatewayOut, Role: RoleStandby,
	}, outPods, outSnapshots)
	if !errors.Is(err, ErrUnsafeState) {
		t.Fatalf("foreign resolveRole() error = %v", err)
	}
}

func routingPod(name string, component Component) PodRef {
	deployment := "mm29-" + string(component)
	return PodRef{
		KubeconfigPath: "/tmp/mm32-kubeconfig",
		ContextName:    "kind-mm32",
		Namespace:      "marketmesh-e2e-tunnel",
		Deployment:     deployment,
		Name:           name,
		UID:            "uid-" + name,
		OwnerRunID:     "mm32-routing",
	}
}

func routingSnapshotForGateway(
	gateway string,
	dc string,
	tunnels []RoutingTunnelSnapshot,
) RoutingSnapshot {
	routes := make([]RoutingRouteSnapshot, 0, len(requiredRoutingRoutes))
	for _, route := range requiredRoutingRoutes {
		current := slices.Clone(tunnels)
		sortRoutingTunnels(current)
		routes = append(routes, RoutingRouteSnapshot{
			Route: route, RouteAllowed: true, Tunnels: current,
		})
	}
	return RoutingSnapshot{
		SchemaVersion:     routingSchemaVersion,
		GatewayInInstance: gateway,
		Routes:            routes,
	}
}

func gatewayOutRoutingSnapshots(t *testing.T, pods []PodRef) []RoutingSnapshot {
	t.Helper()
	first := make([]RoutingTunnelSnapshot, 0, len(pods))
	second := make([]RoutingTunnelSnapshot, 0, len(pods))
	for _, pod := range pods {
		ids := mustGatewayOutIDs(t, pod.Name)
		first = append(first, RoutingTunnelSnapshot{
			InstanceID: ids[0], DataCenter: "dc-a", State: "ready",
		})
		second = append(second, RoutingTunnelSnapshot{
			InstanceID: ids[1], DataCenter: "dc-a", State: "ready",
		})
	}
	return []RoutingSnapshot{
		routingSnapshotForGateway("mm29-gateway-in-a", "dc-a", first),
		routingSnapshotForGateway("mm29-gateway-in-b", "dc-a", second),
	}
}

func mustGatewayOutIDs(t *testing.T, podName string) [gatewayOutSlots]string {
	t.Helper()
	ids, err := gatewayOutInstanceIDs(podName)
	if err != nil {
		t.Fatalf("gatewayOutInstanceIDs() error = %v", err)
	}
	return ids
}

func sortRoutingTunnels(tunnels []RoutingTunnelSnapshot) {
	slices.SortFunc(tunnels, func(left, right RoutingTunnelSnapshot) int {
		if left.InstanceID < right.InstanceID {
			return -1
		}
		if left.InstanceID > right.InstanceID {
			return 1
		}
		return 0
	})
}
