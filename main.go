package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

var ctx = context.Background()
const redisHashKey = "flagship:v1:flags"

type Engine struct {
	mu    sync.RWMutex
	flags map[string]bool
	rdb   *redis.Client
}

func NewEngine(redisAddr, redisPassword string) *Engine {
	rdb := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: redisPassword,
	})

	engine := &Engine{
		flags: make(map[string]bool),
		rdb:   rdb,
	}

	engine.hydrateFromRedis()
	return engine
}

func (e *Engine) hydrateFromRedis() {
	e.mu.Lock()
	defer e.mu.Unlock()

	storedFlags, err := e.rdb.HGetAll(ctx, redisHashKey).Result()
	if err != nil {
		log.Printf("Warning: Failed to hydrate from Redis, starting fresh: %v", err)
		return
	}

	for k, v := range storedFlags {
		e.flags[k] = (v == "true")
	}
	log.Printf("Successfully hydrated %d flags into memory cache using namespace [%s].", len(e.flags), redisHashKey)
}

// CheckRedisConnectivity runs a timed network ping against the cluster instance
func (e *Engine) CheckRedisConnectivity() error {
	// Create a localized 2-second timeout context so a dead network doesn't hang the health check eternally
	pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Executes a native Redis PING command over the socket pool
	return e.rdb.Ping(pingCtx).Err()
}

func (e *Engine) GetFlag(key string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.flags[key]
}

func (e *Engine) SetFlag(key string, value bool) error {
	valStr := "false"
	if value {
		valStr = "true"
	}
	
	if err := e.rdb.HSet(ctx, redisHashKey, key, valStr).Err(); err != nil {
		return fmt.Errorf("redis write failure: %w", err)
	}

	e.mu.Lock()
	e.flags[key] = value
	e.mu.Unlock()

	return nil
}

type FlagPayload struct {
	Key   string `json:"key"`
	Value bool   `json:"value"`
}

func main() {
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	redisPassword := os.Getenv("REDIS_PASSWORD")

	log.Printf("Connecting to Redis target at %s...", redisAddr)
	engine := NewEngine(redisAddr, redisPassword)

	// New Health Endpoint: Checks local container or production Upstash link status
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		
		if err := engine.CheckRedisConnectivity(); err != nil {
			log.Printf("Health check failure: Redis unreachable: %v", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"status": "unhealthy", "redis": "disconnected"}`)
			return
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status": "healthy", "redis": "connected"}`)
	})

	http.HandleFunc("/get", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if key == "" {
			http.Error(w, "Missing 'key' parameter", http.StatusBadRequest)
			return
		}
		enabled := engine.GetFlag(key)
		fmt.Fprintf(w, "Flag [%s]: %t\n", key, enabled)
	})

	http.HandleFunc("/set", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var payload FlagPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "Invalid JSON body", http.StatusBadRequest)
			return
		}
		if err := engine.SetFlag(payload.Key, payload.Value); err != nil {
			http.Error(w, "Internal persistence error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "Successfully updated flag [%s] to %t\n", payload.Key, payload.Value)
	})

	log.Println("Flagship Engine online. Control port listening on :8080...")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalf("Server panic: %v", err)
	}
}