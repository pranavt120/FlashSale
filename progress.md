# FlashSale Learning Progress

## P0: Single-Node Baseline ✓ COMPLETE

### What We Built
- Simple Go HTTP server (~250 lines, no framework)
- Postgres with 10 products (1 hot SKU: product 1)
- Two endpoints:
  - `GET /product?id=N` – Read stock (uses index)
  - `GET /order?id=N` – Buy product (uses FOR UPDATE lock)
- Metrics endpoint showing latency by bucket

### Experiments Run

#### Experiment 1: Baseline (50 VUs)
- **Result:** Low contention, reads ~1ms, writes ~6ms
- **Finding:** Too light to see real bottleneck

#### Experiment 2: High Load (200 VUs)
- **Result:** Lock contention visible
- **Read latency:** 1ms → 71ms (70x slower)
- **Write latency:** 6ms → 210ms avg, p95=595ms
- **RPS:** ~1000 total (~700 reads, ~300 writes)
- **Failures:** 1 read failed (connection/timeout issue)

### Key Learnings

**The Bottleneck:**
- Writes use `FOR UPDATE` to lock product row
- At high load, all threads queue for this lock
- **Reads get slow too** because they share connection pool with slow writes
- Single node maxes out around 1000 RPS with 50 connection pool

**Why This Matters:**
- You can't escape lock cost for inventory correctness
- Connection pooling is a hard limit
- Hot rows serialize all traffic
- Read performance couples to write performance

### Baseline Metrics Captured

```
Load: 200 concurrent users
Duration: 50 seconds
Total Requests: 49,076

Reads (70% of traffic):
  - Count: ~34,400
  - Avg latency: 70.78ms
  - p95: 248.64ms
  - p99: ~1000ms

Writes (30% of traffic):
  - Count: ~14,700
  - Avg latency: 209.79ms
  - p95: 595.22ms
  - p99: ~1600ms

Overall:
  - RPS: ~981
  - Failure rate: 0.002% (1 out of 49,076)
```

### Code Patterns Observed

1. **Connection Pooling:**
   ```go
   db.SetMaxOpenConns(50)  // Hard limit on concurrent DB connections
   db.SetMaxIdleConns(5)
   ```
   Increasing this helped slightly but hit OS/resource limits.

2. **Locking (FOR UPDATE):**
   ```go
   tx.QueryRowContext(ctx, "SELECT stock FROM products WHERE id = $1 FOR UPDATE", id)
   ```
   This is the bottleneck. All traffic serializes here.

3. **Metrics Collection:**
   Used `sync/atomic` for lock-free counters of reads/writes/latency buckets.

### What's NOT Solved Yet

- ❌ Cache (no Redis yet, all reads hit DB)
- ❌ Read replicas (no replication, single master)
- ❌ Hot-row solutions (only raw locking)
- ❌ Sharding (all traffic to one node)
- ❌ Cross-shard transactions
- ❌ Failover & operations

---

## P1: Scaling Reads (Next)

### Plan

Add three layers:
1. **Read replicas** – Postgres streaming replication (async, to see lag)
2. **Cache-aside** – Redis, 60s TTL
3. **Singleflight** – Request coalescing to prevent cache stampedes

### Expected Improvements

- Read latency: 70ms → <10ms (cache) or <50ms (replica)
- Read p95: 248ms → <100ms
- Overall RPS: 981 → 5000+
- Cache hit rate: target 95%+

### New Issues to Discover

1. **Replication lag** – Reads stale data if write hasn't replicated
2. **Cache stampede** – When hot key expires, all requests slam DB
3. **Cache invalidation** – Writes must bust cache (or use CDC)
4. **Thunder herd** – Multiple requests for same cache miss

### Load Test Changes

- Same 200 VUs, 50s duration
- Tag metrics by cache layer (hit/miss/replica)
- Add scenario: write then immediately read (catch replication lag)

---

## Technical Debt / Notes

- `initDB()` now uses `CREATE TABLE IF NOT EXISTS` (was dropping tables)
- Stock for product 1 manually reset to 1M between test runs
- Need to automate baseline capture (JSON metrics endpoint works, could parse)
- k6 metrics tagged by `type:read` / `type:write` (helps isolate problems)

---

## What We Learned About Learning This Way

✓ **What works:**
- Running load tests to see real bottlenecks (not guessing)
- Metrics that show the problem clearly (latency buckets, tagged requests)
- Small enough code to understand each layer
- Progressive increasing of load to hit ceiling

✗ **What's hard:**
- Need high enough load to see realistic bottlenecks (50 VUs wasn't enough)
- Lock contention affects read performance too (not just writes)
- Single machine testing is limited (can't easily test failover, multi-node replication)

---

## Commit History

- `77a2f01` – P0 baseline (rewritten for hands-on learning)
- Next: P1 with replicas + cache-aside
