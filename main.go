package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"flagship/config"
	"flagship/middleware"

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

func (e *Engine) CheckRedisConnectivity() error {
	pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return e.rdb.Ping(pingCtx).Err()
}

func (e *Engine) GetFlag(service, key string) bool {
	compositeKey := fmt.Sprintf("%s:%s", service, key)

	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.flags[compositeKey]
}

func (e *Engine) SetFlag(service, key string, value bool) error {
	compositeKey := fmt.Sprintf("%s:%s", service, key)

	valStr := "false"
	if value {
		valStr = "true"
	}

	if err := e.rdb.HSet(ctx, redisHashKey, compositeKey, valStr).Err(); err != nil {
		return fmt.Errorf("redis write failure: %w", err)
	}

	e.mu.Lock()
	e.flags[compositeKey] = value
	e.mu.Unlock()

	return nil
}

func (e *Engine) GetFlagsByService(service string) map[string]bool {
	prefix := fmt.Sprintf("%s:", service)
	prefixLen := len(prefix)

	e.mu.RLock()
	defer e.mu.RUnlock()

	serviceFlags := make(map[string]bool)

	for compositeKey, value := range e.flags {
		if len(compositeKey) >= prefixLen && compositeKey[:prefixLen] == prefix {
			rawKey := compositeKey[prefixLen:]
			serviceFlags[rawKey] = value
		}
	}

	return serviceFlags
}

type FlagPayload struct {
	Service string `json:"service"`
	Key     string `json:"key"`
	Value   bool   `json:"value"`
}

func main() {
	cfg := config.LoadConfig()

	log.Printf("Connecting to Redis target at %s...", cfg.RedisAddr)
	engine := NewEngine(cfg.RedisAddr, cfg.RedisPassword)

	// Initialize authentication middleware using loaded configurations
	secretBytes := []byte(cfg.JwtSecretKey)
	authGuard := middleware.AuthMiddleware(secretBytes, cfg.JwtAlgorithm)

	// --- PUBLIC ENDPOINTS ---

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

	// --- PROTECTED ENDPOINTS ---
	// We instantiate the routing handlers inside standard http.HandlerFunc wrappers
	// so they can be parsed as a full http.Handler type by our interceptor execution chain.

	// GET Endpoint: Expects /get?service=xampl&key=test_flag
	getHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	setHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	getFlagsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	// 4. Register the protected routes wrapped inside the middleware execution chain
	// http.Handle maps standard interfaces implementing the http.Handler type signature
	http.Handle("/get", authGuard(getHandler))
	http.Handle("/set", authGuard(setHandler))
	http.Handle("/get_flags", authGuard(getFlagsHandler))

	log.Printf("Flagship Engine online [%s mode]. Control port listening on :8080...", cfg.AppEnv)
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalf("Server panic: %v", err)
	}
}
