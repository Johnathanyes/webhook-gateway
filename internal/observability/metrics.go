package observability

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metric collectors register on the default Prometheus registry at package load,
// so any package can record through the helpers below without threading a
// registry around — the same "one place to change" intent as the logger.
var (
	// EventsIngestedTotal counts stored events, split by whether the signature
	// verified. Exported so tests can read it with prometheus testutil.
	EventsIngestedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "webhook_events_ingested_total",
		Help: "Events stored by the ingest endpoint, labeled by signature verification outcome.",
	}, []string{"verified"})

	// DeliveriesTotal counts delivery attempts that reached a terminal-per-attempt
	// state, labeled by outcome (succeeded|failed|dead_lettered).
	DeliveriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "webhook_deliveries_total",
		Help: "Delivery outcomes recorded by the worker, labeled by status.",
	}, []string{"outcome"})

	// DeliveryDurationSeconds is the wall-clock time of each outbound HTTP attempt.
	DeliveryDurationSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "webhook_delivery_duration_seconds",
		Help:    "Duration of outbound delivery HTTP attempts.",
		Buckets: prometheus.DefBuckets,
	})
)

// RecordEventIngested increments the ingest counter for one stored event.
func RecordEventIngested(verified bool) {
	label := "false"
	if verified {
		label = "true"
	}
	EventsIngestedTotal.WithLabelValues(label).Inc()
}

// RecordDelivery increments the delivery counter for one attempt outcome and
// records its duration.
func RecordDelivery(outcome string, durationMs int32) {
	DeliveriesTotal.WithLabelValues(outcome).Inc()
	if durationMs > 0 {
		DeliveryDurationSeconds.Observe(float64(durationMs) / 1000)
	}
}

// MetricsHandler serves the default registry in the Prometheus text format.
func MetricsHandler() http.Handler {
	return promhttp.Handler()
}

var runtimeGaugesOnce sync.Once

// RegisterRuntimeGauges registers DB-backed gauges that are evaluated on each
// scrape: the dead-letter queue size and River's available-job depth. Safe to
// call more than once; only the first call registers.
func RegisterRuntimeGauges(pool *pgxpool.Pool) {
	runtimeGaugesOnce.Do(func() {
		promauto.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "webhook_dead_letter_queue_size",
			Help: "Deliveries currently in the dead_lettered state.",
		}, func() float64 { return scalarCount(pool, "SELECT count(*) FROM deliveries WHERE status = 'dead_lettered'") })

		promauto.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "webhook_queue_depth",
			Help: "River jobs waiting to be worked (state = available).",
		}, func() float64 { return scalarCount(pool, "SELECT count(*) FROM river_job WHERE state = 'available'") })
	})
}

// scalarCount runs a count query with a short timeout, returning 0 on error so a
// transient DB blip degrades the gauge rather than failing the whole scrape.
func scalarCount(pool *pgxpool.Pool, query string) float64 {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var n int64
	if err := pool.QueryRow(ctx, query).Scan(&n); err != nil {
		return 0
	}
	return float64(n)
}
