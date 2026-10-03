package domain

import "errors"

var (
	ErrSeatTaken            = errors.New("seat already taken")
	ErrPerUserLimitExceeded = errors.New("per-user booking limit exceeded")
	ErrIdempotencyConflict  = errors.New("idempotency key conflict: payload mismatch")
	ErrShowNotFound         = errors.New("show not found")
	ErrReservationNotFound  = errors.New("reservation not found")
	ErrForbidden            = errors.New("forbidden: cannot access reservation")
	ErrInvalidInput         = errors.New("invalid input")
	ErrAlreadyCancelled     = errors.New("reservation is already cancelled")
)
