// Package manifests renders the four disposable DC/zone workload sets from
// checked-in Kubernetes templates.
package manifests

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"text/template"
)

const maxValueBytes = 512

var (
	//go:embed dmz.yaml.tmpl
	dmzSource string

	//go:embed internal.yaml.tmpl
	internalSource string
)

// Parameters are the complete bounded inputs needed to render one DC.
type Parameters struct {
	RunID             string
	DC                string
	Version           string
	GatewayInImage    string
	GatewayOutImage   string
	FakeInternalImage string
	GatewayInTarget   string
	GatewayInURI      string
	GatewayOutURI     string
	FakeInternalURI   string
}

// RenderDMZ renders the gateway-in resources for one DMZ cluster.
func RenderDMZ(parameters Parameters) ([]byte, error) {
	if err := validate(parameters, false); err != nil {
		return nil, err
	}

	return render("dmz", dmzSource, parameters)
}

// RenderInternal renders gateway-out and fake-internal for one internal cluster.
func RenderInternal(parameters Parameters) ([]byte, error) {
	if err := validate(parameters, true); err != nil {
		return nil, err
	}

	return render("internal", internalSource, parameters)
}

func render(name string, source string, parameters Parameters) ([]byte, error) {
	parsed, err := template.New(name).Option("missingkey=error").Parse(source)
	if err != nil {
		return nil, fmt.Errorf("manifests: parsing %s template: %w", name, err)
	}
	var output bytes.Buffer
	if err := parsed.Execute(&output, parameters); err != nil {
		return nil, fmt.Errorf("manifests: rendering %s template: %w", name, err)
	}

	return output.Bytes(), nil
}

func validate(parameters Parameters, internal bool) error {
	if err := validateKebab("run id", parameters.RunID); err != nil {
		return err
	}
	if parameters.DC != "dc-a" && parameters.DC != "dc-b" {
		return errors.New("manifests: dc must be dc-a or dc-b")
	}
	if err := validateScalar("version", parameters.Version); err != nil {
		return err
	}
	if err := validateImage("gateway-in image", parameters.GatewayInImage); err != nil {
		return err
	}
	if err := validateURI("gateway-in uri", parameters.GatewayInURI, parameters); err != nil {
		return err
	}
	if err := validateURI("gateway-out uri", parameters.GatewayOutURI, parameters); err != nil {
		return err
	}
	if !internal {
		return nil
	}
	if err := validateImage("gateway-out image", parameters.GatewayOutImage); err != nil {
		return err
	}
	if err := validateImage("fake internal image", parameters.FakeInternalImage); err != nil {
		return err
	}
	if err := validateGatewayTarget(parameters.GatewayInTarget); err != nil {
		return err
	}
	if err := validateURI("fake internal uri", parameters.FakeInternalURI, parameters); err != nil {
		return err
	}

	return nil
}

func validateKebab(name string, value string) error {
	if value == "" || len(value) > 32 || strings.TrimSpace(value) != value {
		return fmt.Errorf("manifests: %s is outside bounds", name)
	}
	for index, character := range []byte(value) {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '-' {
			return fmt.Errorf("manifests: %s must use lower-kebab-case", name)
		}
		if character == '-' && (index == 0 || index == len(value)-1) {
			return fmt.Errorf("manifests: %s must use lower-kebab-case", name)
		}
	}

	return nil
}

func validateScalar(name string, value string) error {
	if value == "" || len(value) > 128 {
		return fmt.Errorf("manifests: %s is outside bounds", name)
	}
	for _, character := range []byte(value) {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '.' && character != '_' && character != '-' {
			return fmt.Errorf("manifests: %s contains an unsafe character", name)
		}
	}

	return nil
}

func validateImage(name string, value string) error {
	if value == "" || len(value) > maxValueBytes {
		return fmt.Errorf("manifests: %s is outside bounds", name)
	}
	for _, character := range []byte(value) {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			!strings.ContainsRune("._:/@-", rune(character)) {
			return fmt.Errorf("manifests: %s contains an unsafe character", name)
		}
	}

	return nil
}

func validateGatewayTarget(value string) error {
	if value == "" || len(value) > maxValueBytes {
		return errors.New("manifests: gateway-in target is outside bounds")
	}
	address := value
	for _, prefix := range []string{"dns:///", "passthrough:///"} {
		if strings.HasPrefix(address, prefix) {
			address = strings.TrimPrefix(address, prefix)
			break
		}
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || port != "30443" {
		return errors.New("manifests: gateway-in target must use the fixed port 30443")
	}
	if parsedPort, err := strconv.Atoi(port); err != nil || parsedPort != 30443 {
		return errors.New("manifests: gateway-in target port is invalid")
	}
	if net.ParseIP(host) == nil {
		if len(host) > 253 || strings.Trim(host, ".") != host {
			return errors.New("manifests: gateway-in target host is invalid")
		}
		for _, character := range []byte(host) {
			if (character < 'a' || character > 'z') &&
				(character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') &&
				character != '.' && character != '-' {
				return errors.New("manifests: gateway-in target host is invalid")
			}
		}
	}

	return nil
}

func validateURI(name string, value string, parameters Parameters) error {
	if value == "" || len(value) > maxValueBytes {
		return fmt.Errorf("manifests: %s is outside bounds", name)
	}
	parsed, err := url.Parse(value)
	wantPrefix := "/e2e/" + parameters.RunID + "/" + parameters.DC + "/"
	if err != nil || parsed.Scheme != "spiffe" || parsed.Host != "marketmesh.test" ||
		!strings.HasPrefix(parsed.Path, wantPrefix) || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("manifests: %s is invalid", name)
	}

	return nil
}
