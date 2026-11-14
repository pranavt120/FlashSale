package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	_ "github.com/lib/pq"
)

var db *sql.DB

type Metrics struct {
	// Reads
	readOps     uint64
	readLatency [5]uint64 // <10ms, <50ms, <100ms, <500ms, >=500ms

	// Writes
	writeOps     uint64
	writeLatency [5]uint64
}

var metrics Metrics

func main() {
	// Connect to Postgres
	dsn := "user=postgres password=postgres dbname=flashsale host=localhost port=5432 sslmode=disable"
	var err error
	db, err = sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Connection pool tuning - this is the FIRST bottleneck you'll hit
	db.SetMaxOpenConns(50)  // Experiment: try 10, then 100, see latency change
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Minute)

	if err := db.Ping(); err != nil {
		log.Fatalf("Ping failed: %v", err)
	}

	// Initialize schema + seed data
	if err := initDB(); err != nil {
		log.Fatalf("Init failed: %v", err)
	}
	log.Printf("✓ DB ready (pool: max_open=50, max_idle=5)")

	// Handlers
	http.HandleFunc("/product", handleProduct)      // Read one product
	http.HandleFunc("/order", handleOrder)          // Write one order (locks inventory)
	http.HandleFunc("/metrics", handleMetrics)      // Live metrics
	http.HandleFunc("/", handleHealth)

	log.Println("Starting on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func initDB() error {
	schema := `
	DROP TABLE IF EXISTS orders CASCADE;
	DROP TABLE IF EXISTS products CASCADE;

	CREATE TABLE products (
		id SERIAL PRIMARY KEY,
		name TEXT,
		stock INT
	);

	CREATE INDEX idx_products_id ON products(id);

	CREATE TABLE orders (
		id BIGINT PRIMARY KEY,
		product_id INT,
		quantity INT,
		created_at TIMESTAMP DEFAULT NOW()
	);
	`

	if _, err := db.Exec(schema); err != nil {
		return err
	}

	// Seed 10 products: 9 with 1000 stock, 1 HOT SKU with 100 stock
	for i := 1; i <= 10; i++ {
		stock := 1000
		if i == 1 {
			stock = 100 // Hot SKU - will create contention
		}
		_, err := db.Exec("INSERT INTO products (id, name, stock) VALUES ($1, $2, $3)", i, fmt.Sprintf("Product %d", i), stock)
		if err != nil {
			return err
		}
	}

	return nil
}

// Read a product (no cache)
func handleProduct(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	id := r.URL.Query().Get("id")
	if id == "" {
		id = "1" // Default to hot SKU
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()

	var stock int
	err := db.QueryRowContext(ctx, "SELECT stock FROM products WHERE id = $1", id).Scan(&stock)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	recordRead(time.Since(start))
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"id":%s,"stock":%d}`, id, stock)
}

// Write an order with inventory lock (FOR UPDATE)
// This is where you'll see lock contention at high load
func handleOrder(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	id := r.URL.Query().Get("id")
	if id == "" {
		id = "1" // Default hits hot SKU
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	// FOR UPDATE locks the row: only one transaction can hold this lock
	// At 1000 RPS, all threads queue here waiting for the lock
	var currentStock int
	err = tx.QueryRowContext(ctx, "SELECT stock FROM products WHERE id = $1 FOR UPDATE", id).Scan(&currentStock)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
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
		tx.Rollback()
		recordWrite(time.Since(start))
		return
	}

	// Insert order
	orderID := time.Now().UnixNano()
	_, err = tx.ExecContext(ctx, "INSERT INTO orders (id, product_id, quantity) VALUES ($1, $2, 1)", orderID, id)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		tx.Rollback()
		recordWrite(time.Since(start))
		return
	}

	if err := tx.Commit(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		recordWrite(time.Since(start))
		return
	}

	recordWrite(time.Since(start))
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"order_id":%d}`, orderID)
}

func handleMetrics(w http.ResponseWriter, r *http.Request) {
	reads := atomic.LoadUint64(&metrics.readOps)
	writes := atomic.LoadUint64(&metrics.writeOps)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{
  "reads": %d,
  "writes": %d,
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
}`, reads, writes,
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
