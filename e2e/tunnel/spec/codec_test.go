package spec_test

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

func TestDecodeScenarioAcceptsAllFailureMatrixFixtures(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob("testdata/scenarios/*.json")
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(paths) != 7 {
		t.Fatalf("scenario fixtures = %d, want 7", len(paths))
	}
	for _, path := range paths {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			data, readErr := readFixture(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			scenario, decodeErr := spec.DecodeScenario(bytes.NewReader(data))
			if decodeErr != nil {
				t.Fatalf("DecodeScenario() error = %v", decodeErr)
			}
			if scenario.SchemaVersion != spec.ScenarioSchemaVersion {
				t.Errorf("schema = %q", scenario.SchemaVersion)
			}
		})
	}
}

func TestDecodeScenarioRejectsAmbiguousJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		json string
		want string
	}{
		{
			name: "unknown field",
			json: `{"schema_version":"marketmesh.tunnel.slo.scenario/v1","id":"x","kind":"planned_rolling_update","warm_up":"0s","targets":[],"faults":[],"unexpected":true}`,
			want: "unknown field",
		},
		{
			name: "duplicate field",
			json: `{"schema_version":"marketmesh.tunnel.slo.scenario/v1","schema_version":"marketmesh.tunnel.slo.scenario/v1","id":"x","kind":"planned_rolling_update","warm_up":"0s","targets":[],"faults":[]}`,
			want: "duplicate json field",
		},
		{
			name: "multiple documents",
			json: `{}` + "\n" + `{}`,
			want: "multiple json documents",
		},
		{
			name: "numeric duration",
			json: `{"schema_version":"marketmesh.tunnel.slo.scenario/v1","id":"x","kind":"planned_rolling_update","warm_up":1,"targets":[],"faults":[]}`,
			want: "duration must be a string",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := spec.DecodeScenario(strings.NewReader(test.json))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodeScenario() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestReportCodecsAreVersionedAndDoNotExposeRequestData(t *testing.T) {
	t.Parallel()

	scenario := plannedScenario()
	report, err := spec.Evaluate(scenario, passingPlannedRun(scenario))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	var jsonOutput bytes.Buffer
	if err := spec.WriteJSONReport(&jsonOutput, report); err != nil {
		t.Fatalf("WriteJSONReport() error = %v", err)
	}
	if !strings.Contains(jsonOutput.String(), spec.ReportSchemaVersion) {
		t.Fatalf("JSON report has no schema version: %s", jsonOutput.String())
	}
	for _, forbidden := range []string{"key-0s", "mutation-0s", "read-0s"} {
		if strings.Contains(jsonOutput.String(), forbidden) {
			t.Fatalf("JSON report contains request data %q", forbidden)
		}
	}
	var decoded spec.Report
	if err := json.Unmarshal(jsonOutput.Bytes(), &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	var junitOutput bytes.Buffer
	if err := spec.WriteJUnitReport(&junitOutput, report); err != nil {
		t.Fatalf("WriteJUnitReport() error = %v", err)
	}
	var suite struct {
		XMLName  xml.Name `xml:"testsuite"`
		Tests    int      `xml:"tests,attr"`
		Failures int      `xml:"failures,attr"`
	}
	if err := xml.Unmarshal(junitOutput.Bytes(), &suite); err != nil {
		t.Fatalf("xml.Unmarshal() error = %v", err)
	}
	if suite.XMLName.Local != "testsuite" || suite.Tests != len(report.Checks) || suite.Failures != 0 {
		t.Fatalf("JUnit suite = %+v", suite)
	}
}

func TestWriteJUnitReportMarksFailedChecks(t *testing.T) {
	t.Parallel()

	scenario := plannedScenario()
	run := passingPlannedRun(scenario)
	run.Requests[1].Missing = true
	run.Requests[1].Attempts = []spec.AttemptObservation{}
	report, err := spec.Evaluate(scenario, run)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	var output bytes.Buffer
	if err := spec.WriteJUnitReport(&output, report); err != nil {
		t.Fatalf("WriteJUnitReport() error = %v", err)
	}
	if !strings.Contains(output.String(), `type="slo_violation"`) ||
		!strings.Contains(output.String(), "missing_requests") {
		t.Fatalf("JUnit report does not contain failure: %s", output.String())
	}
}

func readFixture(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read fixture %s: %w", path, err)
	}
	return data, nil
}
