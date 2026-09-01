//go:build integration

// Package tunneltest exposes the production gateway-in tunnel transport only
// to cross-service integration tests. It is absent from production builds.
package tunneltest

import internaltunnel "github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"

type (
	Call        = internaltunnel.Call
	Config      = internaltunnel.Config
	PeerPolicy  = internaltunnel.PeerPolicy
	QueueLimits = internaltunnel.QueueLimits
	Response    = internaltunnel.Response
	ResultError = internaltunnel.ResultError
	RoutePolicy = internaltunnel.RoutePolicy
	Server      = internaltunnel.Server
)

var New = internaltunnel.New
