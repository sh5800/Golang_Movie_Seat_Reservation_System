package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/sh5800/seat-reservation-service/internal/api"
	"github.com/sh5800/seat-reservation-service/internal/domain"
	"github.com/sh5800/seat-reservation-service/internal/store"
)

func setupTestRouter(t *testing.T) (*store.PostgresStore, http.Handler) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:password@localhost:5432/seat_reservation?sslmode=disable"
	}

	st, err := store.NewPostgresStore(context.Background(), dbURL)
	if err != nil {
		t.Skipf("skipping test: database not reachable: %v", err)
	}

	router := api.NewRouter(st)
	return st, router
}

func createTestShow(t *testing.T, router http.Handler, seats []string) domain.CreateShowResponse {
	payload, _ := json.Marshal(domain.CreateShowRequest{
		Name:       "integration-test-show",
		Seats:      seats,
		PricePaise: 10000,
	})

	req := httptest.NewRequest(http.MethodPost, "/shows", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer admin-tester")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var show domain.CreateShowResponse
	_ = json.NewDecoder(rec.Body).Decode(&show)
	return show
}

// Test 1: Cancel and Re-book + Ownership protection
func TestCancelAndRebook(t *testing.T) {
	_, router := setupTestRouter(t)
	show := createTestShow(t, router, []string{"B01"})

	// Alice reserves B01
	reserveBody, _ := json.Marshal(domain.ReserveRequest{
		Seats:          []string{"B01"},
		IdempotencyKey: "alice-idemp-1",
	})
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/shows/%s/reserve", show.ID), bytes.NewReader(reserveBody))
	req.Header.Set("Authorization", "Bearer user-alice")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("alice reserve failed: %d", rec.Code)
	}

	var res domain.Reservation
	_ = json.NewDecoder(rec.Body).Decode(&res)

	// Bob attempts to cancel Alice's reservation -> MUST BE 403 Forbidden!
	cancelReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/reservations/%s/cancel", res.ID), nil)
	cancelReq.Header.Set("Authorization", "Bearer user-bob")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, cancelReq)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for non-owner cancel, got %d", rec.Code)
	}

	// Alice cancels her own reservation -> MUST BE 200 OK
	cancelReq = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/reservations/%s/cancel", res.ID), nil)
	cancelReq.Header.Set("Authorization", "Bearer user-alice")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, cancelReq)

	if rec.Code != http.StatusOK {
		t.Fatalf("alice cancel failed: %d", rec.Code)
	}

	// Bob can now reserve B01 -> MUST BE 201 Created
	bobReserveBody, _ := json.Marshal(domain.ReserveRequest{
		Seats:          []string{"B01"},
		IdempotencyKey: "bob-idemp-1",
	})
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/shows/%s/reserve", show.ID), bytes.NewReader(bobReserveBody))
	req.Header.Set("Authorization", "Bearer user-bob")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected bob to re-book released seat with 201, got %d", rec.Code)
	}
}

// Test 2: Idempotency Key Conflict (Same key, different payload)
func TestIdempotencyPayloadMismatch(t *testing.T) {
	_, router := setupTestRouter(t)
	show := createTestShow(t, router, []string{"C01", "C02"})

	// 1. Initial reservation with C01
	payload1, _ := json.Marshal(domain.ReserveRequest{
		Seats:          []string{"C01"},
		IdempotencyKey: "tamper-key-1",
	})
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/shows/%s/reserve", show.ID), bytes.NewReader(payload1))
	req.Header.Set("Authorization", "Bearer user-charlie")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("initial reserve failed: %d", rec.Code)
	}

	// 2. Retry with same key but different seat C02 -> MUST BE 409 Conflict
	payload2, _ := json.Marshal(domain.ReserveRequest{
		Seats:          []string{"C02"},
		IdempotencyKey: "tamper-key-1",
	})
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/shows/%s/reserve", show.ID), bytes.NewReader(payload2))
	req.Header.Set("Authorization", "Bearer user-charlie")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for same key with different payload, got %d", rec.Code)
	}
}

// Test 3: Deadlock-free cross-seat race (["D01", "D02"] vs ["D02", "D01"])
func TestDeadlockFreeCrossingRequests(t *testing.T) {
	_, router := setupTestRouter(t)
	show := createTestShow(t, router, []string{"D01", "D02"})

	var wg sync.WaitGroup
	wg.Add(2)

	statusCodes := make([]int, 2)

	// User 1 requests [D01, D02]
	go func() {
		defer wg.Done()
		b, _ := json.Marshal(domain.ReserveRequest{
			Seats:          []string{"D01", "D02"},
			IdempotencyKey: "cross-key-1",
		})
		r := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/shows/%s/reserve", show.ID), bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer user-1")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		statusCodes[0] = rec.Code
	}()

	// User 2 requests [D02, D01] in reverse order!
	go func() {
		defer wg.Done()
		b, _ := json.Marshal(domain.ReserveRequest{
			Seats:          []string{"D02", "D01"},
			IdempotencyKey: "cross-key-2",
		})
		r := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/shows/%s/reserve", show.ID), bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer user-2")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		statusCodes[1] = rec.Code
	}()

	wg.Wait()

	// Neither request should ever return 500 (deadlock)! Exactly one 201, one 409
	has201 := (statusCodes[0] == 201 || statusCodes[1] == 201)
	has409 := (statusCodes[0] == 409 || statusCodes[1] == 409)

	if !has201 || !has409 {
		t.Fatalf("expected one 201 and one 409 without deadlocks, got: %v", statusCodes)
	}
}
