package app

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/services/user/internal/adapter/in/jetstream"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestRegistrationMetricsBoundLabelsAndBacklog(t *testing.T) {
	r := sdkmetric.NewManualReader()
	p := sdkmetric.NewMeterProvider(sdkmetric.WithReader(r))
	defer func() { _ = p.Shutdown(context.Background()) }()
	o, err := newRegistrationObserver(p.Meter("test"))
	if err != nil {
		t.Fatal(err)
	}
	o.outcome(jetstream.Applied)
	o.outcome(jetstream.Outcome("private-identifier"))
	o.backlog(math.MaxUint64, -1)
	o.age(-time.Second)
	var data metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, s := range data.ScopeMetrics {
		for _, m := range s.Metrics {
			seen[m.Name] = true
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, point := range d.DataPoints {
					if point.Attributes.Len() != 1 || strings.Contains(point.Attributes.Encoded(nil), "private") {
						t.Fatal("unbounded/private metric label")
					}
				}
			case metricdata.Gauge[int64]:
				for _, point := range d.DataPoints {
					if point.Attributes.Len() != 0 || point.Value < 0 {
						t.Fatal("invalid backlog metric")
					}
				}
			}
		}
	}
	if len(seen) != 5 {
		t.Fatal("missing metrics", seen)
	}
}
