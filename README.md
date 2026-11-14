# FlashSale: Learning Databases at Million-RPS Scale

A hands-on curriculum for understanding real database bottlenecks through progressive building and breaking.

Each phase follows the same loop: **build minimal code → load test → hit a bottleneck → understand why → fix it → move on**

## P0: Single-Node Baseline (Current)

**Before you code anything, read [P0_GUIDE.md](P0_GUIDE.md).**

**TL;DR:** You run load tests and deliberately break the system to see:
- What happens when connections are exhausted
- How locks serialize writes
- Why indexes matter
- Where the single-node ceiling is

Start:
```bash
docker-compose up -d
go run .
```

Load test (another terminal):
```bash
k6 run load_test_p0.js
```

Watch metrics:
```bash
while true; do curl -s http://localhost:8080/metrics | jq '{reads: .reads, writes: .writes, write_p99: .write_latency_ms.">=500"}'; sleep 1; done
```

Then follow the experiments in P0_GUIDE.md. You should break things and understand why.

---

## Phases

- **P0** – Single node: connection pooling, indexing, FOR UPDATE locks
- **P1** – Read replicas + cache-aside: replication lag, cache stampedes, request coalescing
- **P2** – Hot-row inventory: stock bucketing, Redis Lua scripts
- **P3** – Sharding orders: consistent hashing, scatter-gather, online resharding
- **P4** – Write-heavy counters: batch flushing, LSM vs B-tree
- **P5** – Cross-shard txns: outbox pattern, sagas, idempotency
- **P6** – Failover & ops: Patroni, chaos tests, backups

Each phase is a git branch. Each includes:
- Minimal code (no framework noise)
- Explicit metrics showing the problem
- Load test scenarios
- A guide explaining what to learn

---

## The Stack

- **Language:** Go (no boilerplate, easy to see DB patterns)
- **Database:** Postgres 15
- **Cache:** Redis 7
- **Load testing:** k6
- **Monitoring:** Prometheus + Grafana (optional)
- **Docker:** docker-compose keeps it self-contained

---

## Not Here

- Spring Boot / Django / Express boilerplate
- ORM magic (you write SQL so you see what's happening)
- Canned solutions (you experiment and learn)

---

## Running It

```bash
# Setup
docker-compose up -d
go mod download

# Run server
go run .

# Load test (new terminal)
k6 run load_test_p0.js

# Watch metrics (new terminal)
curl http://localhost:8080/metrics

# Teardown
docker-compose down -v
```

---

## What You'll Learn

By the end, you'll have hands-on experience with:

**Postgres:**
- Connection pooling, resource limits
- Indexes and query planning (`EXPLAIN ANALYZE`)
- Transactions, isolation levels, locking (SELECT ... FOR UPDATE)
- Replication (sync vs async), failover (Patroni)
- Write-Ahead Log (WAL), durability, async commit

**Caching & Consistency:**
- Cache-aside pattern, TTL, stampedes
- Stale-while-revalidate
- Cache invalidation via CDC
- Request coalescing (singleflight)

**Scaling:**
- Read replicas, lag
- Sharding: key selection, consistent hashing, hot shards
- Scatter-gather queries
- Online resharding (no downtime)

**High-Load Patterns:**
- Hot-row solutions (bucketing, Lua scripts, async reconcile)
- Write coalescing (batch updates)
- Outbox pattern, sagas
- Idempotency keys
- Load shedding

---

## Why This Approach?

Most tutorials teach databases in isolation. "Here's a cache", "here's sharding". But in reality:

- Caching breaks consistency
- Sharding breaks transactions
- Locks serialize everything
- Replicas lag

This curriculum shows you **all the tradeoffs at once**, through hands-on experiments. You see the problem before the solution. That's how you build intuition.

---

## Getting Started

1. Read [P0_GUIDE.md](P0_GUIDE.md) — it explains what to measure and what experiments to run
2. Start the stack: `docker-compose up -d`
3. Run the server: `go run .`
4. Run the load test: `k6 run load_test_p0.js`
5. Try the experiments (remove locks, drop indexes, shrink pool)
6. Understand what broke and why
7. When ready, move to P1
