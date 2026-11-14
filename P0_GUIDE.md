# P0: Single-Node Baseline

**Your job:** Run this system under increasing load. Watch it break. Understand why. Fix it.

## What You Have

- `main.go` – ~250 lines. No framework. Just HTTP + Postgres.
- One "hot SKU" (product 1) with 100 units
- Two operations:
  - `GET /product?id=1` – Read stock (uses index)
  - `GET /order?id=1` – Buy product (uses FOR UPDATE lock)
- Metrics endpoint shows read/write latencies

## Experiment 1: Measure Baseline (Connection Pool = 50)

**Setup:**
```bash
docker-compose up -d
go run . &
```

**Load test (in another terminal):**
```bash
k6 run load_test_p0.js
```

**Watch the metrics (in another terminal):**
```bash
watch -n1 'curl -s http://localhost:8080/metrics | jq'
```

**You'll see:**
- Reads: ~5ms latency at 50 concurrent users, ~3500 RPS
- Writes (orders): Latency climbs quickly (why? read on...)
- After 30s: metrics show distribution across latency buckets

**Question to answer:** Why do write latencies spike?

**Hint:** Look at the ORDER handler. It uses `FOR UPDATE`. This locks product 1. Only one transaction can hold the lock at a time. At 50 concurrent users all trying to buy product 1, the rest queue.

---

## Experiment 2: What if you remove the lock?

Edit `handleOrder()` in main.go. Replace:
```go
err = tx.QueryRowContext(ctx, "SELECT stock FROM products WHERE id = $1 FOR UPDATE", id).Scan(&currentStock)
```

With:
```go
err = tx.QueryRowContext(ctx, "SELECT stock FROM products WHERE id = $1", id).Scan(&currentStock)
```

Rebuild, re-run the load test. Watch metrics.

**You'll see:**
- Write latency drops (no waiting for lock!)
- Write throughput increases
- **BUT** now check stock after the test: it's negative. You oversaw.

This teaches: **you can't escape the cost of correctness**. Locks exist for a reason.

---

## Experiment 3: Connection Pool Size

Edit the pool size:
```go
db.SetMaxOpenConns(10)  // Try this
```

Rebuild, load test.

**You'll see:**
- Both read AND write latency spike immediately
- Read latency explodes because every query waits for a connection
- At 50 concurrent users hitting ~1000 RPS combined, only 10 can touch the DB at once

This teaches: **connection pooling is the first bottleneck**. Too small = queueing. Too large = resource exhaustion.

Try `db.SetMaxOpenConns(200)` and see what happens (should be similar to 50).

---

## Experiment 4: Add an Index, Then Remove It

The `/product` endpoint does:
```sql
SELECT stock FROM products WHERE id = $1
```

This uses `idx_products_id`. But what if you drop it?

In a Postgres shell:
```sql
DROP INDEX idx_products_id;
```

Re-run the load test.

**You'll see:**
- Read latency increases (full table scans)
- Eventually reads hit the connection pool and queue behind writes

Recreate the index:
```sql
CREATE INDEX idx_products_id ON products(id);
```

Latency drops again.

This teaches: **Indexes are orthogonal to locks**. A good index makes queries fast; locks make them serialize.

---

## The Key Lesson

**P0 teaches you one thing: WHERE IS THE BOTTLENECK?**

| Bottleneck | Symptom | Fix |
| --- | --- | --- |
| Connection pool too small | All latencies spike evenly | Increase pool |
| Lock contention on hot rows | Write latency >> read latency | Different inventory design (P2) |
| Missing index | Queries slow, then they queue | Add index |
| Single DB node | RPS maxes out ~5-10k | Add replicas (P1) or shards (P3) |

In P0, **you're at the single-node limit**. Reads can be cached (P1). Inventory writes can't (without losing correctness). So you're stuck at ~5-10k RPS before writes become the bottleneck.

---

## Baseline Metrics to Record

Before moving to P1, run this for 1 minute at max comfortable load and capture:

```bash
curl -s http://localhost:8080/metrics | jq '{
  "reads_per_sec": (.reads / 60),
  "writes_per_sec": (.writes / 60),
  "median_read_latency_ms": (.read_latency_ms | to_entries | min_by(.value | tonumber) | .key),
  "p99_write_latency_ms": (.write_latency_ms | to_entries | max_by(.value | tonumber) | .key)
}'
```

Save these numbers. In P1, you'll beat them by adding replicas and caching.

---

## Next: P1 – Scaling Reads

In P0, every read hits the DB (or would, without cache). In P1:
- Add a read replica
- Add Redis with cache-aside
- Watch 95%+ of reads hit Redis
- Manage **replication lag** (reading before write propagates from replica)
- Handle **cache stampedes** (key expires under load → all requests slam DB)
- Implement **request coalescing** to throttle the stampede

Then you'll see: reads can scale to millions. Writes still bottleneck at the lock.
