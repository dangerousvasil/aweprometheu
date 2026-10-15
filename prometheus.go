package aweprometheu

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	name        string
	application string
	segment     string

	sqlRequestTotalName    = "sql_total"
	sqlRequestDurationName = "sql_duration_seconds"

	customLabels                                = []string{"app", "seg", "A", "B", "C", "D"}
	inCounter          *prometheus.CounterVec   = nil
	inGauge            *prometheus.GaugeVec     = nil
	inHistogram        *prometheus.HistogramVec = nil
	inSummary          *prometheus.SummaryVec   = nil
	sqlTotalRequest    *prometheus.CounterVec
	sqlRequestDuration *prometheus.HistogramVec
	Version            string = "handmade"
)

func NewHandler(project, app, seg string) http.Handler {
	name = project
	application = app
	segment = seg

	inCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: name + "_counter_metrics",
	}, customLabels)

	inGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: name + "_gauge_metrics",
	}, customLabels)

	inHistogram = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    name + "_histogram_metrics",
		Buckets: []float64{0.1, 0.25, 0.6, 1.5},
	}, customLabels)

	inSummary = promauto.NewSummaryVec(prometheus.SummaryOpts{
		Name:       name + "_summary_metrics",
		Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
	}, customLabels)

	sqlTotalRequest = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: sqlRequestTotalName,
		Help: "Count of all sql requests",
	}, []string{"sql_query", "batch"})

	sqlRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: sqlRequestDurationName,
		Help: "Duration of all sql requests",
	}, []string{"sql_query", "batch"})

	buildTimestamp, b := os.LookupEnv("CI_COMMIT_SHORT_SHA")
	switch b && buildTimestamp != "" {
	case true:
		CounterAdd(100, "Version", buildTimestamp)
	case false:
		CounterAdd(100, "Version", Version)
	}

	return promhttp.Handler()
}

// Error increments the counter by 1, accepts up to 3 metrics
func Error(labels ...string) {
	if inCounter == nil {
		slog.Debug(`Errors Inc`, "lbls", labels)
	} else {
		inCounter.WithLabelValues(application, segment, `Error`, getLabel(0, labels), getLabel(1, labels), getLabel(2, labels)).Inc()
	}
}

// Counter increments the counter by 1, accepts up to 4 metrics
func Counter(labels ...string) {
	if inCounter == nil {
		slog.Debug(`CounterVec Inc`, "lbls", labels)
	} else {
		inCounter.WithLabelValues(application, segment, getLabel(0, labels), getLabel(1, labels), getLabel(2, labels), getLabel(3, labels)).Inc()
	}
}

// CounterAdd increments the counter by inc, accepts up to 4 metrics
func CounterAdd(inc float64, labels ...string) {
	if inCounter == nil {
		slog.Debug(`CounterVec Set`, "lbls", labels)
	} else {
		inCounter.WithLabelValues(application, segment, getLabel(0, labels), getLabel(1, labels), getLabel(2, labels), getLabel(3, labels)).Add(inc)
	}
}

// Gauge set the Gauge in val, accepts up to 4 metrics
func Gauge(val float64, labels ...string) {
	if inGauge == nil {
		slog.Debug(`GaugeVec Set`, "v", val, "lbls", labels)
	} else {
		inGauge.WithLabelValues(application, segment, getLabel(0, labels), getLabel(1, labels), getLabel(2, labels), getLabel(3, labels)).Set(val)
	}
}

// Histogram set the Histogram in val, accepts up to 4 metrics
func Histogram(val float64, labels ...string) {
	if inHistogram == nil {
		slog.Debug(`HistogramVec Set`, "v", val, "lbls", labels)
	} else {
		inHistogram.WithLabelValues(application, segment, getLabel(0, labels), getLabel(1, labels), getLabel(2, labels), getLabel(3, labels)).Observe(val)
	}
}

// Summary push to exporter  Summary val, accepts up to 4 metrics
func Summary(val float64, labels ...string) {
	if inSummary == nil {
		slog.Debug(`SummaryVec Set`, "v", val, "lbls", labels)
	} else {
		inSummary.WithLabelValues(application, segment, getLabel(0, labels), getLabel(1, labels), getLabel(2, labels), getLabel(3, labels)).Observe(val)
	}
}

func getLabel(i int, labels []string) string {
	if len(labels) > i {
		return labels[i]
	}
	return ""
}

func SetSqlMetrics(sql string, startTime time.Time, batch bool) {
	if sqlRequestDuration == nil {
		slog.Debug("SetSqlMetrics", "sql", sql, "batch", batch, "s", time.Since(startTime))
		return
	}

	Summary(time.Since(startTime).Seconds(), "sql", sql, fmt.Sprintf("%t", batch))

	sqlRequestDuration.WithLabelValues(
		sql,
		strconv.FormatBool(batch),
	).Observe(time.Since(startTime).Seconds())

	sqlTotalRequest.WithLabelValues(
		sql,
		strconv.FormatBool(batch),
	).Inc()
}
