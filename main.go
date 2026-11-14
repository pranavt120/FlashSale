package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

var (
	db *sql.DB
	rc *redis.Client

	// Metrics
	readCount    uint64
	writeCount   uint64
	cacheHits    uint64
	cacheMisses  uint64
	latencyBuckets = [10]uint64{} // 0, 1, 10, 50, 100, 500, 1000, 5000ms, 10000ms, inf
)

func init() {
	log.SetOutput(os.Stdout)
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Connect to Postgres
	psqlconn := "user=postgres password=postgres dbname=flashsale host=localhost port=5432 sslmode=disable"
	var err error
	db, err = sql.Open("postgres", psqlconn)
	if err != nil {
		log.Fatalf("Failed to connect to Postgres: %v", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(100)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(time.Minute)

	// Test connection
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("Postgres ping failed: %v", err)
	}
	log.Println("✓ Connected to Postgres")

	// Connect to Redis
	rc = redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,
	})
	if err := rc.Ping(ctx).Err(); err != nil {
		log.Fatalf("Redis ping failed: %v", err)
	}
	log.Println("✓ Connected to Redis")

	// Initialize schema
	if err := initSchema(ctx); err != nil {
		log.Fatalf("Failed to initialize schema: %v", err)
	}
	log.Println("✓ Schema initialized")

	// HTTP handlers
	http.HandleFunc("/catalog/product", handleGetProduct)
	http.HandleFunc("/orders", handleCreateOrder)
	http.HandleFunc("/metrics", handleMetrics)
	http.HandleFunc("/health", handleHealth)

	// Start server
	go func() {
		log.Println("Starting server on :8080")
		if err := http.ListenAndServe(":8080", nil); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	log.Println("Shutting down...")
	cancel()
}

func initSchema(ctx context.Context) error {
	schema := `
	CREATE TABLE IF NOT EXISTS products (
		id SERIAL PRIMARY KEY,
		name TEXT NOT NULL,
		stock INT NOT NULL,
		price INT NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_products_id ON products(id);

	CREATE TABLE IF NOT EXISTS orders (
		id BIGINT PRIMARY KEY,
		user_id INT NOT NULL,
		product_id INT NOT NULL,
		quantity INT NOT NULL,
		created_at TIMESTAMP DEFAULT NOW(),
		user_id_shard INT NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_orders_user ON orders(user_id);
	CREATE INDEX IF NOT EXISTS idx_orders_created ON orders(created_at DESC);
	`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return err
	}

	// Seed products
	_, err := db.ExecContext(ctx, `
	DELETE FROM orders;
	DELETE FROM products;
	INSERT INTO products (name, stock, price) VALUES
	('GPU RTX 4090', 100, 160000),
	('CPU i9-14900K', 50, 75000),
	('Laptop Gaming', 30, 150000),
	('Monitor 4K', 200, 50000),
	('Keyboard Mech', 500, 15000),
	('Mouse Razer', 1000, 5000),
	('Headphones', 300, 25000),
	('SSD 2TB', 200, 30000),
	('RAM 64GB', 100, 60000),
	('PSU 1000W', 80, 20000)
	ON CONFLICT DO NOTHING;
	`)
	return err
}

func handleGetProduct(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	defer recordLatency(time.Since(start))

	productIDStr := r.URL.Query().Get("id")
	if productIDStr == "" {
		http.Error(w, "Missing id", http.StatusBadRequest)
		return
	}

	productID, err := strconv.Atoi(productIDStr)
	if err != nil {
		http.Error(w, "Invalid id", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
	defer cancel()

	// Try cache first
	cacheKey := fmt.Sprintf("product:%d", productID)
	cachedVal, err := rc.Get(ctx, cacheKey).Result()
	if err == nil {
		atomic.AddUint64(&cacheHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(cachedVal))
		return
	}

	atomic.AddUint64(&cacheMisses, 1)

	// Cache miss: hit DB
	var product struct {
		ID    int    `json:"id"`
		Name  string `json:"name"`
		Stock int    `json:"stock"`
		Price int    `json:"price"`
	}

	err = db.QueryRowContext(ctx, "SELECT id, name, stock, price FROM products WHERE id = $1", productID).
		Scan(&product.ID, &product.Name, &product.Stock, &product.Price)

	if err == sql.ErrNoRows {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("DB error: %v", err), http.StatusInternalServerError)
		return
	}

	// Cache for 60 seconds
	data, _ := json.Marshal(product)
	rc.Set(ctx, cacheKey, string(data), 60*time.Second)

	atomic.AddUint64(&readCount, 1)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(product)
}

func handleCreateOrder(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	defer recordLatency(time.Since(start))

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		UserID    int `json:"user_id"`
		ProductID int `json:"product_id"`
		Quantity  int `json:"quantity"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	// Simple INSERT + UPDATE in one transaction
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		http.Error(w, "Transaction failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	// Check and update stock
	var currentStock int
	err = tx.QueryRowContext(ctx, "SELECT stock FROM products WHERE id = $1 FOR UPDATE", req.ProductID).
		Scan(&currentStock)
	if err != nil {
		http.Error(w, "Product not found", http.StatusNotFound)
		return
	}

	if currentStock < req.Quantity {
		http.Error(w, "Insufficient stock", http.StatusConflict)
		return
	}

	_, err = tx.ExecContext(ctx, "UPDATE products SET stock = stock - $1 WHERE id = $2", req.Quantity, req.ProductID)
	if err != nil {
		http.Error(w, "Failed to update stock", http.StatusInternalServerError)
		return
	}

	// Insert order (simplified ID generation)
	orderID := time.Now().UnixNano()
	_, err = tx.ExecContext(ctx, `
	INSERT INTO orders (id, user_id, product_id, quantity, user_id_shard)
	VALUES ($1, $2, $3, $4, $5)
	`, orderID, req.UserID, req.ProductID, req.Quantity, req.UserID%1024)

	if err != nil {
		http.Error(w, "Failed to create order", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "Commit failed", http.StatusInternalServerError)
		return
	}

	atomic.AddUint64(&writeCount, 1)

	// Invalidate product cache
	cacheKey := fmt.Sprintf("product:%d", req.ProductID)
	rc.Del(ctx, cacheKey)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"order_id": orderID,
	})
}

func handleMetrics(w http.ResponseWriter, r *http.Request) {
	reads := atomic.LoadUint64(&readCount)
	writes := atomic.LoadUint64(&writeCount)
	hits := atomic.LoadUint64(&cacheHits)
	misses := atomic.LoadUint64(&cacheMisses)

	hitRate := float64(0)
	if hits+misses > 0 {
		hitRate = float64(hits) / float64(hits+misses) * 100
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"reads":      reads,
		"writes":     writes,
		"cache_hits": hits,
		"cache_misses": misses,
		"cache_hit_rate": fmt.Sprintf("%.2f%%", hitRate),
	})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func recordLatency(d time.Duration) {
	ms := d.Milliseconds()
	idx := 0
	if ms > 10000 {
		idx = 9
	} else if ms > 5000 {
		idx = 8
	} else if ms > 1000 {
		idx = 7
	} else if ms > 500 {
		idx = 6
	} else if ms > 100 {
		idx = 5
	} else if ms > 50 {
		idx = 4
	} else if ms > 10 {
		idx = 3
	} else if ms > 1 {
		idx = 2
	} else if ms > 0 {
		idx = 1
	}
	atomic.AddUint64(&latencyBuckets[idx], 1)
}
