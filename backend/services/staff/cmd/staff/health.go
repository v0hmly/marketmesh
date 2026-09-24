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
	raw, err := os.ReadFile(os.Getenv("STAFF_CONFIG_FILE"))
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
	identity, err := tls.LoadX509KeyPair("/secrets/health.crt", "/secrets/health.key")
	if err != nil {
		return errors.New("invalid staff health identity")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{identity}}}, Timeout: 2 * time.Second}
	response, err := client.Get(cfg.Origin + "/healthz")
	if err != nil {
		return errors.New("health request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 && response.StatusCode != 204 {
		return errors.New("health endpoint unavailable")
	}
	return nil
}
