# Project Write-Up — Seat Reservation at Scale

**Service**: Movie & Concert Seat Reservation Service  
**Candidate**: Shreyash Kashyap
**Live URL**: [https://golang-movie-seat-reservation-system.onrender.com](https://golang-movie-seat-reservation-system.onrender.com)  
 
---

## 1. The Atomic Decision Mechanism

### Why it is Race-Free

The foundational flaw in naive reservation architectures is the **Read-Then-Write** anti-pattern (`SELECT ... WHERE status = 'available'` followed by `UPDATE`). Under high concurrency, hundreds of concurrent goroutines read `'available'` at the exact same microsecond and issue writes, resulting in double-selling.

To guarantee zero double-selling and avoid database queue starvation, the core state transition is pushed into an **Atomic Conditional Update guarded on current state**:

```sql
UPDATE seats 
SET status = 'confirmed', updated_at = NOW(), version = version + 1
WHERE show_id = $1 
  AND seat_number = ANY($2) 
  AND status = 'available';
```

- In PostgreSQL, row updates acquire row-level exclusive locks within the storage engine.
- When 500 requests race for hot seat `A12`, exactly one transaction succeeds in updating the row; PostgreSQL returns `RowsAffected() == 1`.
- The other 499 transactions immediately evaluate `RowsAffected() == 0`.
- Instead of queueing transactions indefinitely or holding locks, losing transactions immediately abort and return a clean domain decline: `409 Conflict` (reason: `seat-taken`).
- **Result**: Zero 5xx errors, sub-millisecond contention resolution, and absolute prevention of overselling.

### Multi-Seat Deadlock Avoidance

When Request A requests `["A1", "A2"]` and Request B concurrently requests `["A2", "A1"]`, locking rows in arbitrary order produces a circular dependency graph (deadlock), which surfaces as database aborts and 500s.

**Solution**: All requested seat slices are strictly sorted lexicographically in Go (`sort.Strings(seats)`) before executing database operations. Because every concurrent transaction acquires rows in the exact same deterministic sequence ($A1 \to A2$), circular wait conditions are mathematically impossible.

### Per-User Booking Limit Under Concurrency

A critical race condition occurs when a single user fires 10 parallel requests simultaneously on a show with `per_user_limit = 4`. Because all 10 requests read `COUNT(*) = 0` before any transaction commits, naive systems allow all 10 to succeed.

**Solution**: We utilize a PostgreSQL Transaction Advisory Lock scoped to the user and show:

```go
lockKey := hashUserShow(userID, showID)
_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lockKey)
```

- When a single user attempts parallel bookings, requests for that specific `(user, show)` are serialized.
- Requests 1 to 4 succeed; requests 5 to 10 observe `current_count + requested > limit` and are declined with `409 Conflict` (reason: `per-user-limit`).
- Different users hash to distinct 64-bit integer keys, allowing full parallel execution across distinct buyers.
- `pg_advisory_xact_lock` automatically releases upon transaction `COMMIT` or `ROLLBACK`, eliminating any risk of leaked locks.

---

## 2. Idempotency Architecture

### Key Storage & Isolation

Idempotency records are stored in PostgreSQL with a composite primary key:

```sql
CREATE TABLE idempotency_keys (
    key TEXT NOT NULL,
    user_id TEXT NOT NULL,
    show_id UUID NOT NULL REFERENCES shows(id),
    request_hash TEXT NOT NULL,
    response_status INT NOT NULL,
    response_body TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, key)
);
```

### Exactly-Once Semantics

- **User Partitioning**: Partitioning by `(user_id, key)` guarantees that User A supplying key `req-1` can never collide with or block User B supplying key `req-1`.
- **Payload Fingerprinting**: A canonical SHA-256 hash of the sorted seat list is calculated (`request_hash`).
- **Replay vs. Conflict Detection**:
  - **Matching Key & Matching Hash**: Returns the cached response payload with `200 OK` without re-executing booking logic or modifying database state.
  - **Matching Key & Differing Hash**: The user attempted to reuse a key with a different payload. The transaction aborts and returns `409 Conflict` (reason: `idempotency-conflict`).
- **Concurrent Retries**: Because the user-show advisory lock serializes requests from the same user, the initial request writes the record within the reservation transaction, and all concurrent retries immediately read the committed response.

---

## 3. Holds and Cancellation Model

The service implements an explicit cancellation model via `POST /reservations/{id}/cancel`:

- **Identity Enforcement**: Only the owner of a reservation (`token.user_id == reservation.user_id`) can cancel it. Non-owners receive `403 Forbidden`.
- **Atomic Reclaim**: In an atomic transaction, the reservation status transitions to `'cancelled'` and associated seats are reverted to `'available'` with incremented versions.
- **Immediate Re-booking**: A released seat becomes instantly bookable by other users.
- **No Resurrecting Double-Sells**: If a reservation is already cancelled, idempotent cancellation returns `200 OK` without touching active reservations.

---

## 4. Consistency vs. Availability Under Network Partitions (CAP Tradeoff)

For an inventory and monetary transaction system, **Consistency is strictly prioritized over Availability (CP system)**:

- Selling the same seat twice is a catastrophic business and legal failure (overselling / double debit).
- Turning away a buyer with `409 Conflict` or `503 Unavailable` during a network partition is acceptable; double-selling a physical seat is not.
- We utilize PostgreSQL's ACID guarantees and single-leader replication to guarantee linearizable consistency for seat states. In the event of a network partition between the API and database, the service **fails closed** (the `/ready` probe returns `503 Service Unavailable`).

---

## 5. Observability: What We Page for at 2 AM

The service exposes Prometheus-compatible metrics at `/metrics` and structured JSON logs with correlation IDs.

### Critical Alerting Rules (2 AM Pages)

1. **Reconciliation Drift** (`seats_reconciliation_invariant_broken`):
   - `available_seats + confirmed_seats != total_seats`.
   - **Severity**: P0. Indicates state corruption or unhandled rollback failure.

2. **Elevated 5xx Rate** (`http_5xx_rate_high`):
   - `rate(http_requests_total{status=~"5.."}[1m]) / rate(http_requests_total[1m]) > 0.01`.
   - **Severity**: P1. Business declines (409) are expected under load; 5xx errors mean infrastructure or connection exhaustion.

3. **Database Connection Pool Exhaustion** (`pgx_pool_acquire_waits_high`):
   - Active connections exceeding 90% of pool capacity for >30 seconds.
   - **Severity**: P1. Indicates connection saturation or slow queries blocking incoming bursts.

4. **Database Readiness Probe Failures** (`service_readiness_down`):
   - `/ready` returning `503` continuously for >15 seconds.
   - **Severity**: P0. Primary datastore unreachable.

---

## 6. AI Usage Disclosure (Directed vs. Decided)

In accordance with the assigment's guidelines, AI tools were utilized transparently:

- **Directed (Human Decisions)**:
  - **Architecture choice**: Selected Go with PostgreSQL rather than Redis/distributed lock managers to keep the system of record unified in a single transactional database.
  - **Concurrency design**: Chose deterministic lexicographical sorting for multi-seat requests to prevent deadlocks.
  - **Per-user limit enforcement**: Identified that standard `COUNT` queries suffer from phantom reads under concurrency, directing the use of `pg_advisory_xact_lock` for user-scoped serialization.
  - **Atomic conditional update**: Decided on `UPDATE ... WHERE status = 'available'` to eliminate connection pool starvation caused by row-lock queueing.

- **Decided (AI Generation & Assistance)**:
  - Drafting boilerplate HTTP router configurations and Prometheus metric instrumentation.
  - Implementing the concurrent barrier synchronization pattern in `cmd/burst/main.go`.
  - Assisting in troubleshooting Windows application control restrictions and tuning `pgxpool` configurations for cloud container limits.
