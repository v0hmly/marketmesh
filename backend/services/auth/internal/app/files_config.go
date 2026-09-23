package app

import (
	"errors"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"path/filepath"
)

type filesSessionConfig struct {
	enabled                                  bool
	address, certificate, privateKey, rootCA string
	own, peer                                workloadid.Scope
}

func loadFilesSessionConfig(env serviceruntime.Env, environment, trustDomain string) (filesSessionConfig, error) {
	var c filesSessionConfig
	var err error
	c.enabled, err = env.Bool("AUTH_FILES_SCOPED_ENABLED", false)
	if err != nil || !c.enabled {
		return c, err
	}
	for _, field := range []struct {
		name   string
		target *string
	}{
		{"AUTH_FILES_TLS_CERT_FILE", &c.certificate}, {"AUTH_FILES_TLS_KEY_FILE", &c.privateKey}, {"AUTH_FILES_CLIENT_CA_FILE", &c.rootCA},
	} {
		*field.target, err = env.RequiredString(field.name)
		if err != nil {
			return c, err
		}
		if !filepath.IsAbs(*field.target) {
			return c, errors.New("auth files: absolute secret paths required")
		}
	}
	c.address, err = env.String("AUTH_FILES_ADDRESS", "127.0.0.1:9093")
	if err != nil {
		return c, err
	}
	for _, field := range []struct {
		name, role string
		target     *workloadid.Scope
	}{
		{"AUTH_FILES_OWN_URI", "auth", &c.own}, {"AUTH_FILES_EXPECTED_URI", "files", &c.peer},
	} {
		raw, err := env.RequiredString(field.name)
		if err != nil {
			return c, err
		}
		scope, pod, err := workloadid.ParseScopedURI(raw)
		if err != nil || pod != "" || scope.ServiceAccount != field.role || scope.Environment != environment || scope.TrustDomain != trustDomain {
			return c, errors.New("auth files: invalid workload scope")
		}
		*field.target = scope
	}
	return c, nil
}
