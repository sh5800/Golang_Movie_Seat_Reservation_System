package api

import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/sh5800/seat-reservation-service/internal/store"
)

func NewRouter(store *store.PostgresStore) *chi.Mux {
	r := chi.NewRouter()

	// Global middlewares
	r.Use(middleware.Recoverer)
	r.Use(CorrelationIDMiddleware)
	r.Use(MetricsAndLoggingMiddleware)

	h := NewHandler(store)

	// Health and Observability
	r.Get("/live", h.Live)
	r.Get("/ready", h.Ready)
	r.Handle("/metrics", promhttp.Handler())

	// Public / Unauthenticated Show inspection
	r.Get("/shows/{id}", h.GetShow)

	// Admin / Authenticated endpoints
	r.Group(func(admin chi.Router) {
		admin.Use(AuthMiddleware)
		admin.Post("/shows", h.CreateShow)
	})

	// User Authenticated reservation actions
	r.Group(func(user chi.Router) {
		user.Use(AuthMiddleware)
		user.Post("/shows/{id}/reserve", h.ReserveSeats)
		user.Post("/reservations/{id}/cancel", h.CancelReservation)
	})

	return r
}
