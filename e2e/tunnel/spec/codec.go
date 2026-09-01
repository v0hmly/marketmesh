package spec

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	maxScenarioJSONBytes = 1 << 20
	maxRunJSONBytes      = 64 << 20
)

// MarshalJSON encodes a duration as a string such as "250ms" or "30s".
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// UnmarshalJSON decodes a finite, non-negative duration string.
func (d *Duration) UnmarshalJSON(data []byte) error {
	if d == nil {
		return errors.New("spec: nil duration destination")
	}

	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("spec: duration must be a string: %w", err)
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("spec: parse duration: %w", err)
	}
	if parsed < 0 {
		return errors.New("spec: duration must not be negative")
	}
	*d = Duration(parsed)
	return nil
}

// DecodeScenario reads one bounded JSON document, rejects duplicate or unknown
// fields, and validates the complete scenario contract.
func DecodeScenario(reader io.Reader) (Scenario, error) {
	var scenario Scenario
	if err := decodeStrictJSON(reader, maxScenarioJSONBytes, &scenario); err != nil {
		return Scenario{}, fmt.Errorf("spec: decode scenario: %w", err)
	}
	if err := ValidateScenario(scenario); err != nil {
		return Scenario{}, err
	}
	return scenario, nil
}

// DecodeRun reads one bounded JSON ledger and rejects duplicate or unknown fields.
// Semantic completeness is evaluated fail-closed by Evaluate.
func DecodeRun(reader io.Reader) (Run, error) {
	var run Run
	if err := decodeStrictJSON(reader, maxRunJSONBytes, &run); err != nil {
		return Run{}, fmt.Errorf("spec: decode run: %w", err)
	}
	if run.SchemaVersion != RunSchemaVersion {
		return Run{}, fmt.Errorf("spec: unsupported run schema version %q", run.SchemaVersion)
	}
	return run, nil
}

// WriteJSONReport writes one indented JSON report followed by a newline.
func WriteJSONReport(writer io.Writer, report Report) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("spec: encode json report: %w", err)
	}
	return nil
}

// WriteJUnitReport writes a deterministic JUnit testsuite with one testcase per check.
func WriteJUnitReport(writer io.Writer, report Report) error {
	suite := junitSuite{
		Name:  "marketmesh.tunnel.slo/" + report.ScenarioID,
		Tests: len(report.Checks),
		Time:  secondsString(report.EndedAt.Sub(report.StartedAt)),
		Properties: []junitProperty{
			{Name: "schema_version", Value: report.SchemaVersion},
			{Name: "run_id", Value: report.RunID},
			{Name: "status", Value: string(report.Status)},
		},
		TestCases: make([]junitTestCase, 0, len(report.Checks)),
	}

	for _, check := range report.Checks {
		testCase := junitTestCase{
			Name:      check.Name,
			ClassName: "marketmesh.tunnel.slo",
		}
		if !check.Passed {
			suite.Failures++
			testCase.Failure = junitFailureFor(check.Violations)
		}
		suite.TestCases = append(suite.TestCases, testCase)
	}

	if _, err := io.WriteString(writer, xml.Header); err != nil {
		return fmt.Errorf("spec: write junit header: %w", err)
	}
	encoder := xml.NewEncoder(writer)
	encoder.Indent("", "  ")
	if err := encoder.Encode(suite); err != nil {
		return fmt.Errorf("spec: encode junit report: %w", err)
	}
	if _, err := io.WriteString(writer, "\n"); err != nil {
		return fmt.Errorf("spec: finish junit report: %w", err)
	}
	return nil
}

func decodeStrictJSON(reader io.Reader, limit int64, destination any) error {
	if reader == nil {
		return errors.New("nil reader")
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return fmt.Errorf("read json: %w", err)
	}
	if int64(len(data)) > limit {
		return fmt.Errorf("json document exceeds %d bytes", limit)
	}
	if err := rejectDuplicateJSONFields(data); err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	return nil
}

func rejectDuplicateJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := inspectJSONValue(decoder); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func inspectJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}

	switch delimiter {
	case '{':
		fields := map[string]struct{}{}
		for decoder.More() {
			fieldToken, fieldErr := decoder.Token()
			if fieldErr != nil {
				return fieldErr
			}
			field, isString := fieldToken.(string)
			if !isString {
				return errors.New("object field name is not a string")
			}
			if _, exists := fields[field]; exists {
				return fmt.Errorf("duplicate json field %q", field)
			}
			fields[field] = struct{}{}
			if valueErr := inspectJSONValue(decoder); valueErr != nil {
				return valueErr
			}
		}
	case '[':
		for decoder.More() {
			if valueErr := inspectJSONValue(decoder); valueErr != nil {
				return valueErr
			}
		}
	default:
		return fmt.Errorf("unexpected json delimiter %q", delimiter)
	}

	closing, err := decoder.Token()
	if err != nil {
		return err
	}
	expected := json.Delim('}')
	if delimiter == '[' {
		expected = ']'
	}
	if closing != expected {
		return fmt.Errorf("unexpected json closing delimiter %q", closing)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("multiple json documents are not allowed")
}

func secondsString(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	return strconv.FormatFloat(duration.Seconds(), 'f', 6, 64)
}

func junitFailureFor(violations []Violation) *junitFailure {
	codes := make([]string, 0, len(violations))
	messages := make([]string, 0, len(violations))
	for _, violation := range violations {
		codes = append(codes, violation.Code)
		messages = append(messages, violation.Code+": "+violation.Message)
	}
	return &junitFailure{
		Message: strings.Join(codes, ","),
		Type:    "slo_violation",
		Body:    strings.Join(messages, "\n"),
	}
}

type junitSuite struct {
	XMLName    xml.Name        `xml:"testsuite"`
	Name       string          `xml:"name,attr"`
	Tests      int             `xml:"tests,attr"`
	Failures   int             `xml:"failures,attr"`
	Time       string          `xml:"time,attr"`
	Properties []junitProperty `xml:"properties>property"`
	TestCases  []junitTestCase `xml:"testcase"`
}

type junitProperty struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type junitTestCase struct {
	Name      string        `xml:"name,attr"`
	ClassName string        `xml:"classname,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Body    string `xml:",chardata"`
}
