package domain

import (
	"time"

	"github.com/google/uuid"
)

// Seat status constants
const (
	SeatStatusAvailable = "available"
	SeatStatusConfirmed = "confirmed"
)

// Reservation status constants
const (
	ReservationStatusConfirmed = "confirmed"
	ReservationStatusCancelled = "cancelled"
)

// Show represents an event show
type Show struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	PricePaise   int64     `json:"price_paise"`
	PerUserLimit int       `json:"per_user_limit"`
	CreatedAt    time.Time `json:"created_at"`
}

// Seat represents an assigned seat for a show
type Seat struct {
	ID         uuid.UUID `json:"id"`
	ShowID     uuid.UUID `json:"show_id"`
	SeatNumber string    `json:"seat_number"`
	Status     string    `json:"status"`
	Version    int       `json:"version"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Reservation represents a confirmed or cancelled booking
type Reservation struct {
	ID          uuid.UUID `json:"reservation_id"`
	ShowID      uuid.UUID `json:"show_id"`
	UserID      string    `json:"user_id"`
	Seats       []string  `json:"seats"`
	AmountPaise int64     `json:"amount_paise"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

// Request and Response DTOs
type CreateShowRequest struct {
	Name         string   `json:"name"`
	Seats        []string `json:"seats"`
	PricePaise   int64    `json:"price_paise"`
	PerUserLimit *int     `json:"per_user_limit,omitempty"` // Defaults to 4 if not provided
}
type CreateShowResponse struct {
	ID           uuid.UUID   `json:"id"`
	Name         string      `json:"name"`
	PricePaise   int64       `json:"price_paise"`
	PerUserLimit int         `json:"per_user_limit"`
	TotalSeats   int         `json:"total_seats"`
	Seats        []SeatState `json:"seats"`
}
type SeatState struct {
	SeatNumber string `json:"seat_number"`
	Status     string `json:"status"`
}
type ReserveRequest struct {
	Seats          []string `json:"seats"`
	IdempotencyKey string   `json:"idempotency_key"`
}
type ShowStatusResponse struct {
	ShowID         uuid.UUID   `json:"show_id"`
	Name           string      `json:"name"`
	TotalSeats     int         `json:"total_seats"`
	AvailableCount int         `json:"available_count"`
	HeldCount      int         `json:"held_count"` // 0 in immediate-confirm model
	ConfirmedCount int         `json:"confirmed_count"`
	Seats          []SeatState `json:"seats"`
	Reconciled     bool        `json:"reconciled"` // available + held + confirmed == total_seats
}
type ErrorResponse struct {
	Error   string `json:"error"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}
