package networkchaos

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWriteReplayManifestPreservesSeedAndSequenceWithoutImmutableIDs(t *testing.T) {
	t.Parallel()

	degradation := validDegradationFault()
	degradation.Name = "degrade-dc-b"
	degradation.Container = ResourceRef{
		ID:   strings.Repeat("d", 64),
		Name: testRunID + "-gateway-in-b",
	}
	degradation.Network = ResourceRef{
		ID:   strings.Repeat("e", 64),
		Name: testRunID + "-dc-b-dmz",
	}
	plan := Plan{
		Seed: 8675309,
		Steps: []Step{
			{
				Name:   "partition-dc-a",
				Hold:   30 * time.Second,
				Faults: []Fault{validPartitionFault("partition-a", 1)},
			},
			{
				Name:   "degrade-dc-b",
				Hold:   time.Minute,
				Faults: []Fault{degradation},
			},
		},
	}

	var output bytes.Buffer
	if err := WriteReplayManifest(&output, testConfig(), plan); err != nil {
		t.Fatalf("WriteReplayManifest() error = %v", err)
	}
	var manifest replayManifest
	if err := json.Unmarshal(output.Bytes(), &manifest); err != nil {
		t.Fatalf("decoding replay manifest: %v", err)
	}
	if manifest.SchemaVersion != replaySchemaVersion || manifest.Seed != plan.Seed {
		t.Fatalf("replay header = %+v", manifest)
	}
	if len(manifest.Steps) != 2 || manifest.Steps[0].Name != "partition-dc-a" {
		t.Fatalf("replay steps = %+v", manifest.Steps)
	}
	if manifest.Steps[1].Faults[0].DelayMicros != 100_000 {
		t.Fatalf("replay degradation = %+v", manifest.Steps[1].Faults[0])
	}
	for _, immutableID := range []string{
		testContainerRef.ID,
		testNetworkRef.ID,
		testPeerNetworkRef.ID,
		degradation.Container.ID,
		degradation.Network.ID,
	} {
		if strings.Contains(output.String(), immutableID) {
			t.Fatalf("replay contains immutable Docker ID %q", immutableID)
		}
	}
}

func TestWriteReplayManifestRejectsInvalidPlanAndWriterFailure(t *testing.T) {
	t.Parallel()

	if err := WriteReplayManifest(nil, testConfig(), singleStepPlan()); err == nil {
		t.Fatal("WriteReplayManifest() accepted nil destination")
	}
	if err := WriteReplayManifest(&bytes.Buffer{}, testConfig(), Plan{}); err == nil {
		t.Fatal("WriteReplayManifest() accepted empty plan")
	}

	wantErr := errors.New("disk full")
	err := WriteReplayManifest(failingWriter{err: wantErr}, testConfig(), singleStepPlan())
	if !errors.Is(err, wantErr) {
		t.Fatalf("WriteReplayManifest() error = %v, want %v", err, wantErr)
	}
}

type failingWriter struct {
	err error
}

func (writer failingWriter) Write([]byte) (int, error) {
	return 0, writer.err
}
