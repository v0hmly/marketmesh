// Package oidc adapts the maintained coreos OIDC verifier to the staff boundary.
package oidc

import (
	"context"
	"errors"
	"net/http"
	"strings"

	core "github.com/coreos/go-oidc/v3/oidc"
	"github.com/v0hmly/marketmesh/services/staff/internal/application"
	"golang.org/x/oauth2"
)

type Provider struct {
	verifier *core.IDTokenVerifier
	oauth    oauth2.Config
	client   *http.Client
	issuer   string
}

func New(ctx context.Context, issuer, clientID, secret, callback string, client *http.Client) (*Provider, error) {
	provider, err := core.NewProvider(core.ClientContext(ctx, client), issuer)
	if err != nil {
		return nil, errors.New("OIDC discovery failed")
	}
	return &Provider{verifier: provider.Verifier(&core.Config{ClientID: clientID, SupportedSigningAlgs: []string{"RS256"}}), oauth: oauth2.Config{ClientID: clientID, ClientSecret: secret, RedirectURL: callback, Endpoint: provider.Endpoint(), Scopes: []string{core.ScopeOpenID, "email", "profile"}}, client: client, issuer: issuer}, nil
}
func (p *Provider) Authorize(state, nonce, verifier string) string {
	return p.oauth.AuthCodeURL(state, core.Nonce(nonce), oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("prompt", "login"))
}
func (p *Provider) Exchange(ctx context.Context, code, verifier, nonce string) (application.Principal, error) {
	ctx = core.ClientContext(ctx, p.client)
	token, err := p.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return application.Principal{}, errors.New("OIDC exchange failed")
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		return application.Principal{}, errors.New("OIDC ID token missing")
	}
	id, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		return application.Principal{}, errors.New("OIDC ID token rejected")
	}
	if id.Nonce != nonce {
		return application.Principal{}, errors.New("OIDC nonce mismatch")
	}
	var claims struct {
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
		Name     string `json:"name"`
	}
	if err = id.Claims(&claims); err != nil || !claims.Verified || len(claims.Email) > 254 || !strings.Contains(claims.Email, "@") || len(id.Subject) > 255 || id.Subject == "" {
		return application.Principal{}, errors.New("OIDC claims rejected")
	}
	return application.Principal{Issuer: p.issuer, Subject: id.Subject, Email: claims.Email, Name: claims.Name}, nil
}
