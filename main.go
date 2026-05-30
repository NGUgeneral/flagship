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
	pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return e.rdb.Ping(pingCtx).Err()
}

// GetFlag evaluates a key composed from the service context
func (e *Engine) GetFlag(service, key string) bool {
	compositeKey := fmt.Sprintf("%s:%s", service, key)

	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.flags[compositeKey]
}

// SetFlag writes a key composed from the service context into Redis and internal memory
func (e *Engine) SetFlag(service, key string, value bool) error {
	compositeKey := fmt.Sprintf("%s:%s", service, key)

	valStr := "false"
	if value {
		valStr = "true"
	}
	
	// Modifies the exact specific field inside the global Redis Hash
	if err := e.rdb.HSet(ctx, redisHashKey, compositeKey, valStr).Err(); err != nil {
		return fmt.Errorf("redis write failure: %w", err)
	}

	e.mu.Lock()
	e.flags[compositeKey] = value
	e.mu.Unlock()

	return nil
}

// GetFlagsByService scans the memory cache and filters elements matching the service prefix
func (e *Engine) GetFlagsByService(service string) map[string]bool {
	prefix := fmt.Sprintf("%s:", service)
	prefixLen := len(prefix)

	e.mu.RLock()
	defer e.mu.RUnlock()

	// Initialize an empty map to hold the results
	serviceFlags := make(map[string]bool)

	// Loop through your hot-cache memory map
	for compositeKey, value := range e.flags {
		// Go's built-in string package alternative: check if it starts with "service:"
		if len(compositeKey) >= prefixLen && compositeKey[:prefixLen] == prefix {
			// Strip the "xampl:" prefix so the client gets the raw flag name
			rawKey := compositeKey[prefixLen:]
			serviceFlags[rawKey] = value
		}
	}

	return serviceFlags
}

// FlagPayload updated to accept the target client service
type FlagPayload struct {
	Service string `json:"service"`
	Key     string `json:"key"`
	Value   bool   `json:"value"`
}

func main() {
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	redisPassword := os.Getenv("REDIS_PASSWORD")

	log.Printf("Connecting to Redis target at %s...", redisAddr)
	engine := NewEngine(redisAddr, redisPassword)

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

	// GET Endpoint: Expects /get?service=xampl&key=test_flag
	http.HandleFunc("/get", func(w http.ResponseWriter, r *http.Request) {
		service := r.URL.Query().Get("service")
		key := r.URL.Query().Get("key")

		if service == "" || key == "" {
			http.Error(w, "Missing required 'service' or 'key' parameter", http.StatusBadRequest)
			return
		}

		enabled := engine.GetFlag(service, key)
		fmt.Fprintf(w, "Service [%s] Flag [%s]: %t\n", service, key, enabled)
	})

	// POST Endpoint: Expects json body with {"service": "xampl", "key": "test_flag", "value": true}
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

		if payload.Service == "" || payload.Key == "" {
			http.Error(w, "Fields 'service' and 'key' cannot be empty strings", http.StatusBadRequest)
			return
		}

		if err := engine.SetFlag(payload.Service, payload.Key, payload.Value); err != nil {
			http.Error(w, "Internal persistence error", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "Successfully updated flag [%s:%s] to %t\n", payload.Service, payload.Key, payload.Value)
	})

	// GET Endpoint: Expects /get_flags?service=xampl
	http.HandleFunc("/get_flags", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		service := r.URL.Query().Get("service")
		if service == "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error": "Missing required 'service' parameter"}`)
			return
		}

		// Fetch the isolated map from our memory cache
		flagsMatrix := engine.GetFlagsByService(service)

		// Stream the map directly to the network connection as JSON
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(flagsMatrix); err != nil {
			log.Printf("Failed to encode flags matrix payload: %v", err)
		}
	})

	log.Println("Flagship Engine online. Control port listening on :8080...")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalf("Server panic: %v", err)
	}
}