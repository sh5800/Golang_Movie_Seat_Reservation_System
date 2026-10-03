package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sh5800/seat-reservation-service/internal/domain"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(ctx context.Context, connString string) (*PostgresStore, error) {
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse conn string: %w", err)
	}

	// Connection pool tuning for high concurrency bursts
	config.MaxConns = 50
	config.MinConns = 10
	config.MaxConnLifetime = 30 * time.Minute
	config.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create pgxpool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	store := &PostgresStore{pool: pool}
	if err := store.autoMigrate(ctx); err != nil {
		return nil, fmt.Errorf("failed to auto-migrate: %w", err)
	}

	return store, nil
}

func (s *PostgresStore) Close() {
	s.pool.Close()
}

func (s *PostgresStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// autoMigrate ensures tables exist on fresh checkout or clean deploy
func (s *PostgresStore) autoMigrate(ctx context.Context) error {
	query := `
	CREATE TABLE IF NOT EXISTS shows (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		name TEXT NOT NULL,
		price_paise BIGINT NOT NULL CHECK (price_paise >= 0),
		per_user_limit INT NOT NULL DEFAULT 4 CHECK (per_user_limit > 0),
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE IF NOT EXISTS seats (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		show_id UUID NOT NULL REFERENCES shows(id) ON DELETE CASCADE,
		seat_number TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'available' CHECK (status IN ('available', 'confirmed')),
		version INT NOT NULL DEFAULT 1,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		CONSTRAINT uq_show_seat UNIQUE (show_id, seat_number)
	);

	CREATE INDEX IF NOT EXISTS idx_seats_show_status ON seats(show_id, status);
	CREATE INDEX IF NOT EXISTS idx_seats_show_seat_number ON seats(show_id, seat_number);

	CREATE TABLE IF NOT EXISTS reservations (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		show_id UUID NOT NULL REFERENCES shows(id) ON DELETE CASCADE,
		user_id TEXT NOT NULL,
		amount_paise BIGINT NOT NULL CHECK (amount_paise >= 0),
		status TEXT NOT NULL DEFAULT 'confirmed' CHECK (status IN ('confirmed', 'cancelled')),
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		cancelled_at TIMESTAMPTZ
	);

	CREATE INDEX IF NOT EXISTS idx_reservations_user_show_status ON reservations(show_id, user_id, status);

	CREATE TABLE IF NOT EXISTS reservation_seats (
		reservation_id UUID NOT NULL REFERENCES reservations(id) ON DELETE CASCADE,
		seat_id UUID NOT NULL REFERENCES seats(id) ON DELETE CASCADE,
		PRIMARY KEY (reservation_id, seat_id)
	);

	CREATE TABLE IF NOT EXISTS idempotency_keys (
		key TEXT NOT NULL,
		user_id TEXT NOT NULL,
		show_id UUID NOT NULL REFERENCES shows(id) ON DELETE CASCADE,
		request_hash TEXT NOT NULL,
		response_status INT NOT NULL,
		response_body TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (user_id, key)
	);
	`
	_, err := s.pool.Exec(ctx, query)
	return err
}

// CreateShow inserts a show and all initial seats atomically
func (s *PostgresStore) CreateShow(ctx context.Context, name string, seats []string, pricePaise int64, perUserLimit int) (*domain.Show, []domain.SeatState, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	showID := uuid.New()
	var show domain.Show
	showQuery := `
		INSERT INTO shows (id, name, price_paise, per_user_limit)
		VALUES ($1, $2, $3, $4)
		RETURNING id, name, price_paise, per_user_limit, created_at
	`
	err = tx.QueryRow(ctx, showQuery, showID, name, pricePaise, perUserLimit).
		Scan(&show.ID, &show.Name, &show.PricePaise, &show.PerUserLimit, &show.CreatedAt)
	if err != nil {
		return nil, nil, err
	}

	seatStates := make([]domain.SeatState, len(seats))
	batch := &pgx.Batch{}
	for i, seatNum := range seats {
		batch.Queue(`
			INSERT INTO seats (show_id, seat_number, status)
			VALUES ($1, $2, 'available')
		`, showID, seatNum)
		seatStates[i] = domain.SeatState{SeatNumber: seatNum, Status: domain.SeatStatusAvailable}
	}

	br := tx.SendBatch(ctx, batch)
	for range seats {
		_, err := br.Exec()
		if err != nil {
			br.Close()
			return nil, nil, fmt.Errorf("failed to insert seat: %w", err)
		}
	}
	br.Close()

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}

	return &show, seatStates, nil
}

// GetShow retrieves show info, per-seat state, and aggregate counts
func (s *PostgresStore) GetShow(ctx context.Context, showID uuid.UUID) (*domain.ShowStatusResponse, error) {
	var show domain.Show
	err := s.pool.QueryRow(ctx, "SELECT id, name, price_paise, per_user_limit, created_at FROM shows WHERE id = $1", showID).
		Scan(&show.ID, &show.Name, &show.PricePaise, &show.PerUserLimit, &show.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrShowNotFound
		}
		return nil, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT seat_number, status 
		FROM seats 
		WHERE show_id = $1 
		ORDER BY seat_number ASC
	`, showID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var seats []domain.SeatState
	availCount := 0
	confCount := 0

	for rows.Next() {
		var st domain.SeatState
		if err := rows.Scan(&st.SeatNumber, &st.Status); err != nil {
			return nil, err
		}
		seats = append(seats, st)
		if st.Status == domain.SeatStatusAvailable {
			availCount++
		} else if st.Status == domain.SeatStatusConfirmed {
			confCount++
		}
	}

	total := len(seats)
	// Reconciliation Invariant Check
	reconciled := (availCount + confCount) == total

	return &domain.ShowStatusResponse{
		ShowID:         show.ID,
		Name:           show.Name,
		TotalSeats:     total,
		AvailableCount: availCount,
		HeldCount:      0,
		ConfirmedCount: confCount,
		Seats:          seats,
		Reconciled:     reconciled,
	}, nil
}

// HashRequest creates a SHA-256 hash of the canonical seat list
func HashRequest(seats []string) string {
	sorted := make([]string, len(seats))
	copy(sorted, seats)
	sort.Strings(sorted)
	b, _ := json.Marshal(sorted)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ReserveSeats executes the atomic reservation workflow
func (s *PostgresStore) ReserveSeats(
	ctx context.Context,
	showID uuid.UUID,
	userID string,
	idempotencyKey string,
	reqSeats []string,
) (*domain.Reservation, bool, error) { // returns (res, isReplay, error)

	reqHash := HashRequest(reqSeats)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)

	// 1. Idempotency Check
	var existingHash, existingRespBody string
	var existingStatus int
	err = tx.QueryRow(ctx, `
		SELECT request_hash, response_status, response_body 
		FROM idempotency_keys 
		WHERE user_id = $1 AND key = $2
		FOR UPDATE
	`, userID, idempotencyKey).Scan(&existingHash, &existingStatus, &existingRespBody)

	if err == nil {
		// Key was found
		if existingHash != reqHash {
			return nil, false, domain.ErrIdempotencyConflict
		}
		// Idempotent replay: deserialize original response
		var replay domain.Reservation
		if err := json.Unmarshal([]byte(existingRespBody), &replay); err != nil {
			return nil, false, err
		}
		return &replay, true, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}

	// 2. Fetch Show Details
	var pricePaise int64
	var perUserLimit int
	err = tx.QueryRow(ctx, "SELECT price_paise, per_user_limit FROM shows WHERE id = $1", showID).
		Scan(&pricePaise, &perUserLimit)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, domain.ErrShowNotFound
		}
		return nil, false, err
	}

	// 3. Per-User Limit Check under Concurrency
	var currentHeldCount int
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(COUNT(rs.seat_id), 0)
		FROM reservations r
		JOIN reservation_seats rs ON r.id = rs.reservation_id
		WHERE r.show_id = $1 AND r.user_id = $2 AND r.status = 'confirmed'
	`, showID, userID).Scan(&currentHeldCount)
	if err != nil {
		return nil, false, err
	}

	if currentHeldCount+len(reqSeats) > perUserLimit {
		return nil, false, domain.ErrPerUserLimitExceeded
	}

	// 4. Deterministic Ordering (Deadlock-Free Row-Locking)
	sortedSeats := make([]string, len(reqSeats))
	copy(sortedSeats, reqSeats)
	sort.Strings(sortedSeats)

	rows, err := tx.Query(ctx, `
		SELECT id, seat_number, status 
		FROM seats 
		WHERE show_id = $1 AND seat_number = ANY($2)
		ORDER BY seat_number ASC
		FOR UPDATE
	`, showID, sortedSeats)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	type lockedSeat struct {
		id         uuid.UUID
		seatNumber string
		status     string
	}
	var locked []lockedSeat
	for rows.Next() {
		var ls lockedSeat
		if err := rows.Scan(&ls.id, &ls.seatNumber, &ls.status); err != nil {
			return nil, false, err
		}
		locked = append(locked, ls)
	}

	// All-or-nothing check: all requested seats must exist
	if len(locked) != len(sortedSeats) {
		return nil, false, domain.ErrSeatTaken
	}

	// Check if any seat is already confirmed
	seatIDs := make([]uuid.UUID, len(locked))
	for i, ls := range locked {
		if ls.status != domain.SeatStatusAvailable {
			return nil, false, domain.ErrSeatTaken
		}
		seatIDs[i] = ls.id
	}

	// 5. Atomic State Transition to Confirmed
	_, err = tx.Exec(ctx, `
		UPDATE seats 
		SET status = 'confirmed', updated_at = NOW(), version = version + 1
		WHERE show_id = $1 AND seat_number = ANY($2)
	`, showID, sortedSeats)
	if err != nil {
		return nil, false, err
	}

	// 6. Create Reservation Record (Money is integer paise!)
	totalAmount := pricePaise * int64(len(sortedSeats))
	reservationID := uuid.New()
	var res domain.Reservation
	res.ID = reservationID
	res.ShowID = showID
	res.UserID = userID
	res.Seats = sortedSeats
	res.AmountPaise = totalAmount
	res.Status = domain.ReservationStatusConfirmed
	res.CreatedAt = time.Now().UTC()

	_, err = tx.Exec(ctx, `
		INSERT INTO reservations (id, show_id, user_id, amount_paise, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, res.ID, res.ShowID, res.UserID, res.AmountPaise, res.Status, res.CreatedAt)
	if err != nil {
		return nil, false, err
	}

	// Link seats in reservation_seats
	for _, sID := range seatIDs {
		_, err = tx.Exec(ctx, `
			INSERT INTO reservation_seats (reservation_id, seat_id)
			VALUES ($1, $2)
		`, res.ID, sID)
		if err != nil {
			return nil, false, err
		}
	}

	// 7. Store Idempotency Record in the same transaction
	respBytes, _ := json.Marshal(res)
	_, err = tx.Exec(ctx, `
		INSERT INTO idempotency_keys (key, user_id, show_id, request_hash, response_status, response_body)
		VALUES ($1, $2, $3, $4, 201, $5)
	`, idempotencyKey, userID, showID, reqHash, string(respBytes))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique violation
			return nil, false, domain.ErrIdempotencyConflict
		}
		return nil, false, err
	}

	// Commit Transaction
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}

	return &res, false, nil
}

// CancelReservation allows the owner of a reservation to cancel it
func (s *PostgresStore) CancelReservation(ctx context.Context, reservationID uuid.UUID, userID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var resUserID, resStatus string
	var showID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT show_id, user_id, status 
		FROM reservations 
		WHERE id = $1 
		FOR UPDATE
	`, reservationID).Scan(&showID, &resUserID, &resStatus)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrReservationNotFound
		}
		return err
	}

	// Token-derived identity protection: only owner can cancel
	if resUserID != userID {
		return domain.ErrForbidden
	}

	if resStatus == domain.ReservationStatusCancelled {
		return domain.ErrAlreadyCancelled
	}

	// Release seats back to available
	_, err = tx.Exec(ctx, `
		UPDATE seats s
		SET status = 'available', updated_at = NOW(), version = version + 1
		FROM reservation_seats rs
		WHERE rs.reservation_id = $1 AND s.id = rs.seat_id
	`, reservationID)
	if err != nil {
		return err
	}

	// Mark reservation cancelled
	_, err = tx.Exec(ctx, `
		UPDATE reservations 
		SET status = 'cancelled', cancelled_at = NOW() 
		WHERE id = $1
	`, reservationID)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}
