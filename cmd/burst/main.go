package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sh5800/seat-reservation-service/internal/domain"
)

type BurstStats struct {
	TotalRequests        int64
	Status201            int64
	Status200Replay      int64
	Status409Conflict    int64
	Status5xx            int64
	DeclineSeatTaken     int64
	DeclineUserLimit     int64
	DeclineIdempMismatch int64
	OtherCodes           map[int]int64
	mu                   sync.Mutex
}

func newStats() *BurstStats {
	return &BurstStats{
		OtherCodes: make(map[int]int64),
	}
}

func (s *BurstStats) Record(statusCode int, body []byte) {
	atomic.AddInt64(&s.TotalRequests, 1)

	switch statusCode {
	case http.StatusCreated:
		atomic.AddInt64(&s.Status201, 1)
	case http.StatusOK:
		atomic.AddInt64(&s.Status200Replay, 1)
	case http.StatusConflict:
		atomic.AddInt64(&s.Status409Conflict, 1)
		var errResp domain.ErrorResponse
		if err := json.Unmarshal(body, &errResp); err == nil {
			switch errResp.Reason {
			case "seat-taken":
				atomic.AddInt64(&s.DeclineSeatTaken, 1)
			case "per-user-limit":
				atomic.AddInt64(&s.DeclineUserLimit, 1)
			case "idempotency-conflict":
				atomic.AddInt64(&s.DeclineIdempMismatch, 1)
			}
		}
	default:
		if statusCode >= 500 {
			atomic.AddInt64(&s.Status5xx, 1)
			// Print unexpected 5xx message for instant visibility
			fmt.Printf("⚠️  5xx Error [%d]: %s\n", statusCode, string(body))
		} else {
			s.mu.Lock()
			s.OtherCodes[statusCode]++
			s.mu.Unlock()
		}
	}
}

func main() {
	baseURL := "http://localhost:8080"
	if len(os.Args) > 1 {
		baseURL = os.Args[1]
	}

	fmt.Println("==============================================================")
	fmt.Printf("🚀 Starting Concurrency Burst Test against: %s\n", baseURL)
	fmt.Println("==============================================================")

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        2000,
			MaxIdleConnsPerHost: 2000,
			MaxConnsPerHost:     2000,
			IdleConnTimeout:     30 * time.Second,
		},
	}

	// 1. Health check verification
	resp, err := client.Get(baseURL + "/ready")
	if err != nil || resp.StatusCode != http.StatusOK {
		fmt.Printf("❌ Target server is not ready at %s/ready: %v\n", baseURL, err)
		os.Exit(1)
	}
	resp.Body.Close()
	fmt.Println("✅ Server readiness check passed!")

	// 2. Create a test show with 50 seats
	seats := make([]string, 50)
	for i := 1; i <= 50; i++ {
		seats[i-1] = fmt.Sprintf("A%02d", i)
	}

	createShowPayload, _ := json.Marshal(domain.CreateShowRequest{
		Name:         "burst-test-event",
		Seats:        seats,
		PricePaise:   25000,
		PerUserLimit: intPtr(4),
	})

	req, _ := http.NewRequest(http.MethodPost, baseURL+"/shows", bytes.NewReader(createShowPayload))
	req.Header.Set("Authorization", "Bearer admin-token")
	req.Header.Set("Content-Type", "application/json")

	createResp, err := client.Do(req)
	if err != nil || createResp.StatusCode != http.StatusCreated {
		fmt.Printf("❌ Failed to create show: %v (status: %d)\n", err, createResp.StatusCode)
		os.Exit(1)
	}

	var show domain.CreateShowResponse
	_ = json.NewDecoder(createResp.Body).Decode(&show)
	createResp.Body.Close()

	fmt.Printf("✅ Created test show ID: %s with %d seats (limit: %d)\n\n", show.ID, show.TotalSeats, show.PerUserLimit)

	stats := newStats()

	// -------------------------------------------------------------------------
	// TEST 1: HOT SEAT STORM (500 users all racing for seat "A12" at t=0)
	// -------------------------------------------------------------------------
	concurrency := 500
	fmt.Printf("🔥 Storming Hot Seat 'A12' with %d concurrent buyers at t=0...\n", concurrency)

	var wg sync.WaitGroup
	startBarrier := make(chan struct{})

	for i := 1; i <= concurrency; i++ {
		wg.Add(1)
		go func(buyerID int) {
			defer wg.Done()
			payload, _ := json.Marshal(domain.ReserveRequest{
				Seats:          []string{"A12"},
				IdempotencyKey: fmt.Sprintf("storm-key-%d", buyerID),
			})

			// Wait for the release barrier so all goroutines fire concurrently
			<-startBarrier

			r, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/shows/%s/reserve", baseURL, show.ID), bytes.NewReader(payload))
			r.Header.Set("Authorization", fmt.Sprintf("Bearer buyer-%d", buyerID))
			r.Header.Set("Content-Type", "application/json")

			res, err := client.Do(r)
			if err != nil {
				stats.Record(599, nil)
				return
			}
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			stats.Record(res.StatusCode, b)
		}(i)
	}

	startTime := time.Now()
	close(startBarrier) // FIRE all 500 requests simultaneously!
	wg.Wait()
	duration := time.Since(startTime)
	fmt.Printf("⏱️  Hot seat burst completed in %v\n\n", duration)

	// -------------------------------------------------------------------------
	// TEST 2: IDEMPOTENT RETRIES (50 concurrent retries with the SAME key)
	// -------------------------------------------------------------------------
	fmt.Println("🔁 Testing Idempotent Retries (50 concurrent calls with identical key)...")
	startBarrier2 := make(chan struct{})
	for i := 1; i <= 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload, _ := json.Marshal(domain.ReserveRequest{
				Seats:          []string{"A01"},
				IdempotencyKey: "idempotent-shared-key-100",
			})

			<-startBarrier2

			r, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/shows/%s/reserve", baseURL, show.ID), bytes.NewReader(payload))
			r.Header.Set("Authorization", "Bearer user-alice")
			r.Header.Set("Content-Type", "application/json")

			res, err := client.Do(r)
			if err != nil {
				stats.Record(599, nil)
				return
			}
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			stats.Record(res.StatusCode, b)
		}()
	}
	close(startBarrier2)
	wg.Wait()
	fmt.Println("✅ Idempotent retries completed!\n")

	// -------------------------------------------------------------------------
	// TEST 3: PER-USER LIMIT STRESS (1 user fires 10 parallel reserves for limit=4)
	// -------------------------------------------------------------------------
	fmt.Println("🛡️  Testing Per-User Limit (1 user attempting 10 concurrent seat reserves)...")
	startBarrier3 := make(chan struct{})
	for i := 1; i <= 10; i++ {
		wg.Add(1)
		go func(seatIndex int) {
			defer wg.Done()
			payload, _ := json.Marshal(domain.ReserveRequest{
				Seats:          []string{fmt.Sprintf("A%02d", 20+seatIndex)},
				IdempotencyKey: fmt.Sprintf("limit-test-key-%d", seatIndex),
			})

			<-startBarrier3

			r, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/shows/%s/reserve", baseURL, show.ID), bytes.NewReader(payload))
			r.Header.Set("Authorization", "Bearer user-greedy")
			r.Header.Set("Content-Type", "application/json")

			res, err := client.Do(r)
			if err != nil {
				stats.Record(599, nil)
				return
			}
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			stats.Record(res.StatusCode, b)
		}(i)
	}
	close(startBarrier3)
	wg.Wait()
	fmt.Println("✅ Per-user limit stress completed!\n")

	// -------------------------------------------------------------------------
	// RECONCILIATION INVARIANT CHECK (GET /shows/{id})
	// -------------------------------------------------------------------------
	showResp, err := client.Get(fmt.Sprintf("%s/shows/%s", baseURL, show.ID))
	if err != nil {
		fmt.Printf("❌ Failed to query show state: %v\n", err)
		os.Exit(1)
	}
	var finalShow domain.ShowStatusResponse
	_ = json.NewDecoder(showResp.Body).Decode(&finalShow)
	showResp.Body.Close()

	// Print Outcome Summary
	fmt.Println("==============================================================")
	fmt.Println("📊 BURST OUTCOME DISTRIBUTION")
	fmt.Println("==============================================================")
	fmt.Printf("Total Requests Fired     : %d\n", stats.TotalRequests)
	fmt.Printf("✅ 201 Created (Wins)    : %d\n", stats.Status201)
	fmt.Printf("🔁 200 OK (Idemp Replay) : %d\n", stats.Status200Replay)
	fmt.Printf("🚫 409 Conflict (Declined): %d\n", stats.Status409Conflict)
	fmt.Printf("   ├─ Seat Taken         : %d\n", stats.DeclineSeatTaken)
	fmt.Printf("   ├─ Per-User Limit     : %d\n", stats.DeclineUserLimit)
	fmt.Printf("   └─ Idemp Conflict     : %d\n", stats.DeclineIdempMismatch)
	fmt.Printf("💥 5xx Server Errors     : %d\n", stats.Status5xx)

	fmt.Println("\n==============================================================")
	fmt.Println("⚖️  FINAL RECONCILIATION INVARIANT CHECK")
	fmt.Println("==============================================================")
	fmt.Printf("Total Seats              : %d\n", finalShow.TotalSeats)
	fmt.Printf("Available Seats          : %d\n", finalShow.AvailableCount)
	fmt.Printf("Held Seats               : %d\n", finalShow.HeldCount)
	fmt.Printf("Confirmed Seats          : %d\n", finalShow.ConfirmedCount)
	fmt.Printf("Invariant Formula        : available(%d) + held(%d) + confirmed(%d) == total(%d)\n",
		finalShow.AvailableCount, finalShow.HeldCount, finalShow.ConfirmedCount, finalShow.TotalSeats)

	reconciled := (finalShow.AvailableCount + finalShow.HeldCount + finalShow.ConfirmedCount) == finalShow.TotalSeats
	if reconciled && stats.Status5xx == 0 {
		fmt.Println("\n🎯 RESULT: PASSED! Invariant holds exactly and zero 5xx errors recorded!")
	} else {
		fmt.Println("\n❌ RESULT: FAILED! Invariant broken or 5xx errors encountered.")
		os.Exit(1)
	}
	fmt.Println("==============================================================")
}

func intPtr(i int) *int {
	return &i
}
