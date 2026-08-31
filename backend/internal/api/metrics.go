package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metrics are the counters a hosted deployment needs to answer "is it healthy"
// without reading logs.
type metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	// activeSupportGrants is the one that matters for trust: an operator, and
	// an auditor, can see at a glance how often support is inside band data.
	activeSupportGrants prometheus.Gauge
	registry            *prometheus.Registry
}

var (
	metricsOnce   sync.Once
	sharedMetrics *metrics
)

func newMetrics() *metrics {
	metricsOnce.Do(func() {
		registry := prometheus.NewRegistry()
		m := &metrics{
			registry: registry,
			requests: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "merch_http_requests_total",
				Help: "HTTP requests by method, route and status.",
			}, []string{"method", "route", "status"}),
			duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Name:    "merch_http_request_duration_seconds",
				Help:    "HTTP request duration by route.",
				Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
			}, []string{"route"}),
			activeSupportGrants: prometheus.NewGauge(prometheus.GaugeOpts{
				Name: "merch_active_support_grants",
				Help: "Support access grants currently open on band data.",
			}),
		}
		registry.MustRegister(m.requests, m.duration, m.activeSupportGrants)
		sharedMetrics = m
	})
	return sharedMetrics
}

// observe records one finished request.
//
// The route template is used rather than the raw path, so a receipt ID never
// becomes its own metric label and blows up cardinality.
func (m *metrics) observe(c *gin.Context, started time.Time) {
	route := c.FullPath()
	if route == "" {
		route = "unmatched"
	}
	m.requests.WithLabelValues(c.Request.Method, route, http.StatusText(c.Writer.Status())).Inc()
	m.duration.WithLabelValues(route).Observe(time.Since(started).Seconds())
}

// metricsMiddleware records every request.
func (s *Server) metricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		s.metrics.observe(c, started)
	}
}

// metricsHandler exposes the Prometheus endpoint.
func (s *Server) metricsHandler() gin.HandlerFunc {
	handler := promhttp.HandlerFor(s.metrics.registry, promhttp.HandlerOpts{})
	return func(c *gin.Context) {
		handler.ServeHTTP(c.Writer, c.Request)
	}
}
