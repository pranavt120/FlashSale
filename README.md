# FlashSale: Learning Databases at Million-RPS Scale

A progressive curriculum for learning real database architecture by building a flash sale backend that handles millions of requests per second.

## P0: Single-Node Baseline (Current)

**Goal:** Measure the absolute ceiling of one Postgres + Redis node, understand connection pooling and indexing, identify the first bottleneck.

### What's Here

- **main.go** – Dead-simple Go HTTP server with two endpoints:
  - `GET /catalog/product?id=N` – Read from cache, fallback to DB (cache-aside pattern)
  - `POST /orders` – Write order with inventory check (transaction with FOR UPDATE)
  - `GET /metrics` – Basic counters
  
- **Stack:**
  - Postgres 15 (no fancy replication yet)
  - Redis 7 (basic cache)
  - k6 (load testing)
  - Prometheus + Grafana (metrics, optional)

- **load_test_p0.js** – Ramps up 100 concurrent users, measures latency/throughput

### Key Concepts in P0

1. **Connection Pooling**: The app limits Postgres to 100 open connections. If you remove `SetMaxOpenConns`, watch latency explode as Postgres refuses new connections.

2. **Indexes**: Product queries scan the index `idx_products_id`. Try removing it and re-running the load test; latency will tank.

3. **Cache-Aside Pattern**: `GET /catalog/product` tries Redis first. On miss, it hits the DB and backfills the cache. Watch cache hit rate climb.

4. **Transactions with Locking**: `POST /orders` uses `FOR UPDATE` to lock the product row while checking stock. This prevents overselling but creates contention at high load.

5. **WAL & Durability**: Every order write goes through Postgres's Write-Ahead Log. Async vs sync replication will matter in P6.

### Running P0

**Setup:**
```bash
docker-compose up -d          # Start Postgres, Redis, Prometheus
go build -o flashsale .
./flashsale
```

**Load test:**
```bash
# Terminal 2
k6 run --vus 50 --duration 60s load_test_p0.js
```

**Observe:**
- Terminal 1: Watch app logs for connection/query errors
- Terminal 2: k6 shows p95/p99 latencies and throughput
- `curl http://localhost:8080/metrics` – See cache hit rate, read/write counts

### What You'll See

- **Cache hit rate:** Should climb from 0% to ~95% as hot products get cached
- **Latency at 50 VUs:** Reads ≈ 5-20ms (cache), ≈ 50-100ms (DB). Writes ≈ 200-500ms (locks).
- **RPS ceiling:** ~5-10k RPS before latency blows up (limited by single DB node)

### What to Measure

Before moving to P1, run the load test and capture:
- **Peak reads RPS** (until p99 > 1s)
- **Peak writes RPS** (until p99 > 1s)
- **Cache hit ratio** at peak
- **CPU/memory usage** (watch Docker)

If you remove the cache (`// rc.Set(...)`), re-run and see how read throughput tanks.

### Key Code to Study

- **Cache pattern**: `handleGetProduct()` – Try/catch Redis, fallback to DB, backfill
- **Locking**: `handleCreateOrder()` – `FOR UPDATE` prevents lost updates
- **Metrics**: `atomic.AddUint64()` – Lock-free counters for high-frequency updates
- **Latency tracking**: `recordLatency()` – Simple bucket histogram

### Next: P1 – Scaling Reads

P1 adds read replicas and fixes cache stampedes (key expires under load → all traffic slams DB). You'll learn:
- Replication lag issues (reading before write propagates)
- Cache invalidation strategies
- Singleflight / request coalescing
- Stale-while-revalidate

When ready, `git commit` your P0 measurements and branch for P1.
