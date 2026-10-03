package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/go-chi/chi/v5"
	"github.com/sh5800/seat-reservation-service/internal/domain"
	"github.com/sh5800/seat-reservation-service/internal/store"
)

type Handler struct {
	store *store.PostgresStore
}

func NewHandler(store *store.PostgresStore) *Handler {
	return &Handler{
		store: store,
	}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (h *Handler) Live(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
}

func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "unavailable",
			"error":  "database ping failed",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// CreateShow: POST /shows
func (h *Handler) CreateShow(w http.ResponseWriter, r *http.Request) {
	var req domain.CreateShowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, domain.ErrorResponse{
			Error:   "bad_request",
			Message: "invalid JSON body",
		})
		return
	}
	if req.Name == "" || len(req.Seats) == 0 || req.PricePaise < 0 {
		writeJSON(w, http.StatusBadRequest, domain.ErrorResponse{
			Error:   "bad_request",
			Message: "name, non-empty seats list, and valid price_paise (>=0) are required",
		})
		return
	}
	perUserLimit := 4
	if req.PerUserLimit != nil && *req.PerUserLimit > 0 {
		perUserLimit = *req.PerUserLimit
	}
	show, seats, err := h.store.CreateShow(r.Context(), req.Name, req.Seats, req.PricePaise, perUserLimit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, domain.ErrorResponse{Error: "internal_error", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, domain.CreateShowResponse{
		ID:           show.ID,
		Name:         show.Name,
		PricePaise:   show.PricePaise,
		PerUserLimit: show.PerUserLimit,
		TotalSeats:   len(seats),
		Seats:        seats,
	})
}

// GetShow: GET /shows/{id}
func (h *Handler) GetShow(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	showID, err := uuid.Parse(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, domain.ErrorResponse{Error: "bad_request", Message: "invalid show id"})
		return
	}
	res, err := h.store.GetShow(r.Context(), showID)
	if err != nil {
		if errors.Is(err, domain.ErrShowNotFound) {
			writeJSON(w, http.StatusNotFound, domain.ErrorResponse{Error: "not_found", Message: "show not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, domain.ErrorResponse{Error: "internal_error", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ReserveSeats: POST /shows/{id}/reserve
func (h *Handler) ReserveSeats(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	showID, err := uuid.Parse(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, domain.ErrorResponse{Error: "bad_request", Message: "invalid show id"})
		return
	}
	userID, _ := r.Context().Value(UserIDContextKey).(string)
	var req domain.ReserveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, domain.ErrorResponse{Error: "bad_request", Message: "invalid JSON body"})
		return
	}
	// Idempotency key can come from header or body
	idempKey := req.IdempotencyKey
	if idempKey == "" {
		idempKey = r.Header.Get("Idempotency-Key")
	}
	if idempKey == "" || len(req.Seats) == 0 {
		writeJSON(w, http.StatusBadRequest, domain.ErrorResponse{
			Error:   "bad_request",
			Message: "idempotency_key and non-empty seats array are required",
		})
		return
	}
	res, isReplay, err := h.store.ReserveSeats(r.Context(), showID, userID, idempKey, req.Seats)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrSeatTaken):
			ReservationsDeclinedTotal.WithLabelValues("seat-taken").Inc()
			writeJSON(w, http.StatusConflict, domain.ErrorResponse{
				Error:   "seat_taken",
				Reason:  "seat-taken",
				Message: "one or more requested seats are already taken or invalid",
			})
		case errors.Is(err, domain.ErrPerUserLimitExceeded):
			ReservationsDeclinedTotal.WithLabelValues("per-user-limit").Inc()
			writeJSON(w, http.StatusConflict, domain.ErrorResponse{
				Error:   "limit_exceeded",
				Reason:  "per-user-limit",
				Message: "booking exceeds allowed seats per user limit",
			})
		case errors.Is(err, domain.ErrIdempotencyConflict):
			ReservationsDeclinedTotal.WithLabelValues("idempotency-conflict").Inc()
			writeJSON(w, http.StatusConflict, domain.ErrorResponse{
				Error:   "idempotency_conflict",
				Reason:  "idempotency-conflict",
				Message: "same idempotency key supplied with different payload",
			})
		case errors.Is(err, domain.ErrShowNotFound):
			ReservationsDeclinedTotal.WithLabelValues("show-not-found").Inc()
			writeJSON(w, http.StatusNotFound, domain.ErrorResponse{
				Error:   "not_found",
				Message: "show not found",
			})
		default:
			writeJSON(w, http.StatusInternalServerError, domain.ErrorResponse{
				Error:   "internal_error",
				Message: err.Error(),
			})
		}
		return
	}
	if isReplay {
		IdempotentReplaysTotal.Inc()
		writeJSON(w, http.StatusOK, res)
		return
	}
	ReservationsConfirmedTotal.Inc()
	writeJSON(w, http.StatusCreated, res)
}

// CancelReservation: POST /reservations/{id}/cancel
func (h *Handler) CancelReservation(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	resID, err := uuid.Parse(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, domain.ErrorResponse{Error: "bad_request", Message: "invalid reservation id"})
		return
	}
	userID, _ := r.Context().Value(UserIDContextKey).(string)
	err = h.store.CancelReservation(r.Context(), resID, userID)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrReservationNotFound):
			writeJSON(w, http.StatusNotFound, domain.ErrorResponse{Error: "not_found", Message: "reservation not found"})
		case errors.Is(err, domain.ErrForbidden):
			writeJSON(w, http.StatusForbidden, domain.ErrorResponse{Error: "forbidden", Message: "cannot cancel reservation owned by another user"})
		case errors.Is(err, domain.ErrAlreadyCancelled):
			writeJSON(w, http.StatusOK, map[string]string{"message": "reservation is already cancelled"})
		default:
			writeJSON(w, http.StatusInternalServerError, domain.ErrorResponse{Error: "internal_error", Message: err.Error()})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"message": "reservation cancelled successfully and seats released",
	})
}
