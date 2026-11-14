# FlashSale: Learning Databases at Million-RPS Scale

Nov 14, 2026 · @Pranav

**The use case:** you build the backend for a Big Billion Days–style flash sale. At 12:00:00, 10M users arrive at once. This one system covers almost every hard database problem: heavy reads, hot rows, write-heavy counters, sharded orders, transactions across shards, and failover.

## 1. Traffic targets (do this math first)

| Flow | Peak | Character |
| --- | --- | --- |
| Product/catalog reads | 2M RPS | Read-heavy, a few very hot keys |
| View/like counters | 1M writes/s | Write-heavy, losing a few is OK |
| Buy attempts on the top 100 SKUs | 200k RPS | Extreme contention, no overselling allowed |
| Orders created | 50k/s | Durable, sharded |
| Wallet debits | 50k/s | Strongly consistent, idempotent |

**Rough capacity per node:** one Postgres node handles about 20–50k simple queries/s, or about 5–10k durable writes/s. One Redis node handles about 100–200k ops/s. So:

- **Reads:** you need a cache hit rate of at least 95%, which leaves ≤100k RPS for the database.
- **Orders:** you need roughly 8–16 write shards.
- **Inventory:** no single row can absorb 200k RPS, so you need a different design.

**A frank caveat:** a laptop won't reach millions of RPS. Measure one node's ceiling, prove that adding shards scales throughput roughly linearly, then extrapolate. That is how it's done in real capacity planning too.

## 2. Stack

- **App:** Go or Spring Boot
- **Database:** Postgres + PgBouncer + Patroni
- **Cache:** Redis Cluster
- **Change stream:** Kafka + Debezium (CDC)
- **Counters:** ScyllaDB or Cassandra
- **Load testing:** k6
- **Metrics:** Prometheus + Grafana
- **Running it:** docker-compose

## 3. Phases

Each phase follows the same loop: build it, break it under load, then fix it.

### P0 – Single-node baseline

- **Build:** catalog + orders on one Postgres instance.
- **Break it:** ramp k6 until p99 latency blows up.
- **Concepts:** connection pooling (why 10k connections kill Postgres), `EXPLAIN ANALYZE`, covering indexes, WAL, group commit, `synchronous_commit` trade-offs.
- **Deliverable:** your measured per-node read and write ceilings.

### P1 – Scaling reads (catalog, 2M RPS)

- **Build:** read replicas, then cache-aside with Redis.
- **Break it:**
  - Expire a hot key under load. You get a stampede.
  - Point 50% of traffic at one SKU. One Redis shard overheats.
  - Write, then read straight from a replica. You read stale data because of replication lag.
- **Fixes:** singleflight/request coalescing, stale-while-revalidate, TTL jitter, an in-process L1 cache for hot keys, read-your-writes routing. Do cache invalidation through CDC, not by having the app write to both the database and the cache.

### P2 – Hot-row inventory (200k RPS on one SKU)

Implement four approaches and measure each one's throughput and oversell count:

1. `SELECT ... FOR UPDATE`. Watch it collapse.
2. Atomic `UPDATE stock SET qty = qty - 1 WHERE id = ? AND qty > 0`.
3. Stock bucketing: split the stock into N sub-rows and pick one at random.
4. Redis Lua script for an atomic decrement, plus an async reservation and reconcile step back to the database.

**Concepts:** isolation levels, lost updates, lock contention, and the fact that the source of truth and the place you enforce the rule can be different systems.

### P3 – Sharding orders (50k writes/s)

- **Build:**
  - Shard key = `user_id`, mapped to 1024 logical shards that sit on N physical nodes.
  - A routing layer in the app.
  - Snowflake-style IDs that carry the shard number.
- **Break it:**
  - "Show all orders for seller X" now has to query every shard (scatter-gather).
  - One big seller creates a skewed shard.
- **Fixes:**
  - A global secondary index (orders by seller) built from CDC into a separate store.
  - Online resharding from 4 to 8 nodes: backfill, dual-read, cutover. Done live, with no errors in the k6 run.
- **Concepts:** hash vs range partitioning, consistent hashing, choosing a shard key, local vs global indexes.

### P4 – Write-heavy counters (1M writes/s)

- **Build:** Redis `INCR` with a periodic batched flush, compared against Scylla counters.
- **Concepts:** write coalescing, LSM trees vs B-trees under write load, compaction, accepting approximate counts.

### P5 – Transactions across shards (order + wallet + inventory)

- **Build:** the outbox pattern, a saga with compensating steps, idempotency keys on every write.
- **Break it:** kill the app between the wallet debit and the order insert.
- **Pass condition:** no double charge, no lost order. Also explain why you're not using two-phase commit (2PC).

### P6 – Failure and operations

- **Build:**
  - Patroni failover; measure how many writes you lose with async vs sync replicas.
  - Cassandra quorum (R + W > N).
  - Backups and point-in-time recovery (PITR).
  - Load shedding and rate limiting at the edge.
  - Hedged requests to cut p99 latency.
- **Chaos tests:** kill the primary, partition the network, fill the disk.

## 4. What to track in every phase

- p50/p99 latency
- Throughput per node
- Cache hit ratio
- Replication lag
- Shard skew
- Oversell count (must be 0)
- Data lost on failover

The concept that ties it all together: **every fix moves the bottleneck somewhere else.** Being able to name where it moved is what interviewers mean by senior-level thinking.

## 5. Concepts you'll have covered when done

Pooling, indexing, WAL/durability, replication (sync vs async, lag), caching patterns and failure modes, hot keys, contention, isolation levels, partitioning, resharding, global indexes, CDC, LSM vs B-tree, quorum, outbox/saga/idempotency, failover, backpressure.
