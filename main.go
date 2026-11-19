package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	_ "github.com/lib/pq"
	"golang.org/x/sync/singleflight"
)

var (
	dbMaster   *sql.DB // Write endpoint
	dbReplica  *sql.DB // Read endpoint
	redisClient *redis.Client
	sfGroup     singleflight.Group // Request coalescing for cache misses
)

type Metrics struct {
	readOps       uint64
	readLatency   [5]uint64 // <10ms, <50ms, <100ms, <500ms, >=500ms
	cacheHits     uint64
	cacheMisses   uint64
	cacheStamps   uint64
	replicaLag    int64 // milliseconds

	writeOps      uint64
	writeLatency  [5]uint64
}

var metrics Metrics

func main() {
	// Master: writes only (port 5432)
	dsn := "user=postgres password=postgres dbname=flashsale host=localhost port=5432 sslmode=disable"
	var err error
	dbMaster, err = sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("Master open failed: %v", err)
	}
	defer dbMaster.Close()
	dbMaster.SetMaxOpenConns(50)
	dbMaster.SetMaxIdleConns(5)
	dbMaster.SetConnMaxLifetime(time.Minute)

	// Replica: reads only (port 5433)
	dsn = "user=postgres password=postgres dbname=flashsale host=localhost port=5433 sslmode=disable"
	dbReplica, err = sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("Replica open failed: %v", err)
	}
	defer dbReplica.Close()
	dbReplica.SetMaxOpenConns(50)
	dbReplica.SetMaxIdleConns(5)
	dbReplica.SetConnMaxLifetime(time.Minute)

	// Redis cache (60s TTL)
	redisClient = redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	defer redisClient.Close()

	if err := redisClient.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("Redis ping failed: %v", err)
	}

	if err := dbMaster.Ping(); err != nil {
		log.Fatalf("Master ping failed: %v", err)
	}

	if err := dbReplica.Ping(); err != nil {
		log.Fatalf("Replica ping failed: %v", err)
	}

	// Initialize schema on master only
	if err := initDB(); err != nil {
		log.Fatalf("Init failed: %v", err)
	}
	log.Printf("✓ Master ready (port 5432, pool: 50)")
	log.Printf("✓ Replica ready (port 5433, pool: 50)")
	log.Printf("✓ Redis ready (localhost:6379)")

	// Handlers
	http.HandleFunc("/product", handleProduct)      // Read from cache/replica
	http.HandleFunc("/order", handleOrder)          // Write to master, invalidate cache
	http.HandleFunc("/metrics", handleMetrics)      // Live metrics
	http.HandleFunc("/", handleHealth)

	log.Println("Starting on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func initDB() error {
	// Schema only created on master; replica gets it via replication
	schema := `
	CREATE TABLE IF NOT EXISTS products (
		id SERIAL PRIMARY KEY,
		name TEXT,
		stock INT
	);

	CREATE INDEX IF NOT EXISTS idx_products_id ON products(id);

	CREATE TABLE IF NOT EXISTS orders (
		id BIGINT PRIMARY KEY,
		product_id INT,
		quantity INT,
		created_at TIMESTAMP DEFAULT NOW()
	);
	`

	if _, err := dbMaster.Exec(schema); err != nil {
		return err
	}

	// Seed products on master
	for i := 1; i <= 10; i++ {
		var exists bool
		dbMaster.QueryRow("SELECT EXISTS(SELECT 1 FROM products WHERE id = $1)", i).Scan(&exists)
		if exists {
			continue
		}

		stock := 1000
		if i == 1 {
			stock = 1000000 // Hot SKU
		}
		_, err := dbMaster.Exec("INSERT INTO products (id, name, stock) VALUES ($1, $2, $3)", i, fmt.Sprintf("Product %d", i), stock)
		if err != nil {
			return err
		}
	}

	return nil
}

// handleProduct: cache-aside pattern
// 1. Check Redis cache
// 2. On miss, use singleflight to coalesce requests
// 3. Query replica (read-only, behind master)
// 4. Update cache with 60s TTL
func handleProduct(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	id := r.URL.Query().Get("id")
	if id == "" {
		id = "1"
	}

	// Try cache first
	cacheKey := fmt.Sprintf("product:%s", id)
	cached, err := redisClient.Get(context.Background(), cacheKey).Result()
	if err == nil {
		// Cache hit
		atomic.AddUint64(&metrics.cacheHits, 1)
		recordRead(time.Since(start))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "HIT")
		fmt.Fprint(w, cached)
		return
	}

	// Cache miss: use singleflight to coalesce requests for same key
	// If 10 requests come in simultaneously for key "1", only 1 hits DB
	val, err, shared := sfGroup.Do(cacheKey, func() (interface{}, error) {
		atomic.AddUint64(&metrics.cacheMisses, 1)

		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()

		var stock int
		err := dbReplica.QueryRowContext(ctx, "SELECT stock FROM products WHERE id = $1", id).Scan(&stock)
		if err != nil {
			return nil, err
		}

		result := fmt.Sprintf(`{"id":%s,"stock":%d}`, id, stock)

		// Update cache with 60s TTL
		redisClient.Set(context.Background(), cacheKey, result, 60*time.Second)

		return result, nil
	})

	// shared=true means other requests were coalesced (cache stampede mitigation)
	if shared {
		atomic.AddUint64(&metrics.cacheStamps, 1)
	}

	if err != nil {
		recordRead(time.Since(start))
		w.WriteHeader(http.StatusNotFound)
		return
	}

	recordRead(time.Since(start))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", "MISS")
	fmt.Fprint(w, val.(string))
}

// handleOrder: write to master, invalidate cache
func handleOrder(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	id := r.URL.Query().Get("id")
	if id == "" {
		id = "1"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	tx, err := dbMaster.BeginTx(ctx, nil)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		recordWrite(time.Since(start))
		return
	}
	defer tx.Rollback()

	// FOR UPDATE lock on master
	var currentStock int
	err = tx.QueryRowContext(ctx, "SELECT stock FROM products WHERE id = $1 FOR UPDATE", id).Scan(&currentStock)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		recordWrite(time.Since(start))
		return
	}

	if currentStock <= 0 {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"error":"out of stock"}`)
		tx.Rollback()
		recordWrite(time.Since(start))
		return
	}

	// Decrease stock
	_, err = tx.ExecContext(ctx, "UPDATE products SET stock = stock - 1 WHERE id = $1", id)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		recordWrite(time.Since(start))
		return
	}

	// Insert order
	orderID := time.Now().UnixNano()
	_, err = tx.ExecContext(ctx, "INSERT INTO orders (id, product_id, quantity) VALUES ($1, $2, 1)", orderID, id)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		recordWrite(time.Since(start))
		return
	}

	if err := tx.Commit(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		recordWrite(time.Since(start))
		return
	}

	// Invalidate cache for this product (write hits master, invalidate immediately)
	cacheKey := fmt.Sprintf("product:%s", id)
	redisClient.Del(context.Background(), cacheKey)

	recordWrite(time.Since(start))
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"order_id":%d}`, orderID)
}

func handleMetrics(w http.ResponseWriter, r *http.Request) {
	reads := atomic.LoadUint64(&metrics.readOps)
	writes := atomic.LoadUint64(&metrics.writeOps)
	cacheHits := atomic.LoadUint64(&metrics.cacheHits)
	cacheMisses := atomic.LoadUint64(&metrics.cacheMisses)
	cacheStamps := atomic.LoadUint64(&metrics.cacheStamps)

	cacheHitRate := 0.0
	if totalReads := cacheHits + cacheMisses; totalReads > 0 {
		cacheHitRate = float64(cacheHits) / float64(totalReads) * 100
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{
  "reads": %d,
  "writes": %d,
  "cache_hits": %d,
  "cache_misses": %d,
  "cache_stampedes": %d,
  "cache_hit_rate_pct": %.2f,
  "read_latency_ms": {
    "<10": %d,
    "<50": %d,
    "<100": %d,
    "<500": %d,
    ">=500": %d
  },
  "write_latency_ms": {
    "<10": %d,
    "<50": %d,
    "<100": %d,
    "<500": %d,
    ">=500": %d
  }
}`, reads, writes, cacheHits, cacheMisses, cacheStamps, cacheHitRate,
		atomic.LoadUint64(&metrics.readLatency[0]),
		atomic.LoadUint64(&metrics.readLatency[1]),
		atomic.LoadUint64(&metrics.readLatency[2]),
		atomic.LoadUint64(&metrics.readLatency[3]),
		atomic.LoadUint64(&metrics.readLatency[4]),
		atomic.LoadUint64(&metrics.writeLatency[0]),
		atomic.LoadUint64(&metrics.writeLatency[1]),
		atomic.LoadUint64(&metrics.writeLatency[2]),
		atomic.LoadUint64(&metrics.writeLatency[3]),
		atomic.LoadUint64(&metrics.writeLatency[4]),
	)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "ok")
}

func recordRead(d time.Duration) {
	atomic.AddUint64(&metrics.readOps, 1)
	recordLatency(d, &metrics.readLatency)
}

func recordWrite(d time.Duration) {
	atomic.AddUint64(&metrics.writeOps, 1)
	recordLatency(d, &metrics.writeLatency)
}

func recordLatency(d time.Duration, buckets *[5]uint64) {
	ms := d.Milliseconds()
	idx := 4
	if ms < 10 {
		idx = 0
	} else if ms < 50 {
		idx = 1
	} else if ms < 100 {
		idx = 2
	} else if ms < 500 {
		idx = 3
	}
	atomic.AddUint64(&buckets[idx], 1)
}
