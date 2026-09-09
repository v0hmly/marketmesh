package registrationmetrics

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/services/auth/internal/application/publishregistration"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestDeliveryMetricsNeverCarryIdentityLabels(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = provider.Shutdown(context.Background()) }()
	observer, err := New(provider.Meter("test"))
	if err != nil {
		t.Fatal(err)
	}
	observer.ObserveBacklog(t.Context(), 3, 2*time.Second)
	observer.Attempt(t.Context(), publishregistration.OutcomePublished)
	observer.Attempt(t.Context(), publishregistration.Outcome("private-subject"))
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, scope := range metrics.ScopeMetrics {
		for _, m := range scope.Metrics {
			if data, ok := m.Data.(metricdata.Sum[int64]); ok {
				for _, point := range data.DataPoints {
					found = true
					attrs := point.Attributes.ToSlice()
					if len(attrs) != 1 || string(attrs[0].Key) != "outcome" || strings.Contains(attrs[0].Value.AsString(), "private") {
						t.Fatal("unsafe metric attributes", attrs)
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("delivery metrics missing")
	}
}
