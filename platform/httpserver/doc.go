// Package httpserver создаёт безопасно ограниченные net/http servers и
// связывает их с lifecycle MarketMesh.
//
// Пакет отвечает за transport timeouts, пределы headers/body, request deadline,
// panic recovery, безопасные logs, OpenTelemetry и HTTP health adapters. Router,
// application handlers, TLS и listener остаются явными обязанностями сервиса.
package httpserver
