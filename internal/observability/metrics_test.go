package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordMetricsMoveCounters(t *testing.T) {
	before := testutil.ToFloat64(EventsIngestedTotal.WithLabelValues("true"))
	RecordEventIngested(true)
	if got := testutil.ToFloat64(EventsIngestedTotal.WithLabelValues("true")); got != before+1 {
		t.Errorf("events ingested (verified) = %v, want %v", got, before+1)
	}

	beforeD := testutil.ToFloat64(DeliveriesTotal.WithLabelValues("dead_lettered"))
	RecordDelivery("dead_lettered", 250)
	if got := testutil.ToFloat64(DeliveriesTotal.WithLabelValues("dead_lettered")); got != beforeD+1 {
		t.Errorf("deliveries (dead_lettered) = %v, want %v", got, beforeD+1)
	}
}

func TestMetricsHandlerExposesSeries(t *testing.T) {
	RecordEventIngested(false)
	RecordDelivery("succeeded", 10)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"webhook_events_ingested_total",
		"webhook_deliveries_total",
		"webhook_delivery_duration_seconds",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics output missing %q", want)
		}
	}
}
