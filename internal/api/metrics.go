package api

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	ReservationsConfirmedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "reservations_confirmed_total",
		Help: "Number of reservations successfully confirmed",
	})

	ReservationsDeclinedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "reservations_declined_total",
		Help: "The total number of reservations declined, partitioned by reason",
	}, []string{"reason"})

	IdempotentReplaysTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "reservation_idempotent_replays_total",
		Help: "The total number of requests served as idempotent replays",
	})

	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests processed, partitioned by method, path, and status code",
	}, []string{"method", "path", "status"})

	HTTPRequestDurationSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency distributions in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})
)
