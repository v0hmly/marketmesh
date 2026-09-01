// Package podchaos orchestrates sequential, fail-closed tunnel pod outages.
//
// Kubernetes access, continuous probe accounting, diagnostics persistence and
// disposable topology lifecycle remain behind explicit caller-owned adapters.
// The package never selects a current kube context, starts goroutines or cleans
// topology resources.
package podchaos
