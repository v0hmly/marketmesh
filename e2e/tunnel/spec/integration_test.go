//go:build integration

package spec_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

func TestJSONLedgerToJSONAndJUnitReports(t *testing.T) {
	t.Parallel()

	scenarioJSON, err := json.Marshal(plannedScenario())
	if err != nil {
		t.Fatalf("json.Marshal(scenario) error = %v", err)
	}
	scenario, err := spec.DecodeScenario(bytes.NewReader(scenarioJSON))
	if err != nil {
		t.Fatalf("DecodeScenario() error = %v", err)
	}
	runJSON, err := json.Marshal(passingPlannedRun(scenario))
	if err != nil {
		t.Fatalf("json.Marshal(run) error = %v", err)
	}
	run, err := spec.DecodeRun(bytes.NewReader(runJSON))
	if err != nil {
		t.Fatalf("DecodeRun() error = %v", err)
	}

	report, err := spec.Evaluate(scenario, run)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	var jsonReport bytes.Buffer
	if err := spec.WriteJSONReport(&jsonReport, report); err != nil {
		t.Fatalf("WriteJSONReport() error = %v", err)
	}
	var junitReport bytes.Buffer
	if err := spec.WriteJUnitReport(&junitReport, report); err != nil {
		t.Fatalf("WriteJUnitReport() error = %v", err)
	}
	if !strings.Contains(jsonReport.String(), `"status": "pass"`) {
		t.Fatalf("JSON report = %s", jsonReport.String())
	}
	if strings.Contains(junitReport.String(), "<failure") {
		t.Fatalf("JUnit report = %s", junitReport.String())
	}
}
