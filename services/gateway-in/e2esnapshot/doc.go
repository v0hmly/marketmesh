// Package e2esnapshot defines the bounded, E2E-only routing snapshot wire
// format exposed by gateway-in when its explicit test switch is enabled.
//
// Opaque instance identifiers are allowed only in this response. Callers must
// not copy them into logs, metrics, traces, errors, or long-lived artifacts.
package e2esnapshot
