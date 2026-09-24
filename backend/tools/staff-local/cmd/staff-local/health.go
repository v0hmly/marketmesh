package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"
)

func health() error {
	raw, err := os.ReadFile(os.Getenv("OIDC_CONFIG_FILE"))
	if err != nil {
		return err
	}
	var cfg struct{ Origin string }
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	pem, err := os.ReadFile("/secrets/ca.pem")
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return errors.New("invalid health CA")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}}, Timeout: 2 * time.Second}
	response, err := client.Get("https://oidc.localhost:18445/.well-known/openid-configuration")
	if err != nil {
		return errors.New("health request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 && response.StatusCode != 204 {
		return errors.New("health endpoint unavailable")
	}
	return nil
}
