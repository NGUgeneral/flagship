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

// Accept the full config struct so we can inspect both RedisURL and local configuration fields
func NewEngine(cfg *config.Config) *Engine {
	var opt *redis.Options
	var err error

	// 1. If the single unified Redis URL is active (Production / Upstash)
	if cfg.RedisURL != "" {
		opt, err = redis.ParseURL(cfg.RedisURL)
		if err != nil {
			log.Fatalf("CRITICAL: Failed to parse secure Redis connection URL: %v", err)
		}
		log.Println("🔒 Redis client initialized securely via connection URL string (TLS Enabled).")
	} else {
		// 2. Fallback cleanly to classic separate parameters for unencrypted local Docker environments
		opt = &redis.Options{
			Addr:     cfg.RedisAddr,
			Password: cfg.RedisPassword,
			DB:       0,
		}
		log.Printf("🔓 Redis client initialized via unencrypted parameters. Target: %s", cfg.RedisAddr)
	}

	rdb := redis.NewClient(opt)

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
	log.Printf("📢 DEBUG RUNTIME ENV: REDIS_URL length is %d", len(os.Getenv("REDIS_URL")))
	cfg := config.LoadConfig()
	log.Printf("📢 DEBUG RUNTIME ENV: cfg REDIS_URL length is %d", len(cfg.RedisURL))

	// Initialize engine with the complete configuration context
	engine := NewEngine(cfg)

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

	getFlagsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		service := r.URL.Query().Get("service")
		if service == "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error": "Missing required 'service' parameter"}`)
			return
		}

		flagsMatrix := engine.GetFlagsByService(service)

		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(flagsMatrix); err != nil {
			log.Printf("Failed to encode flags matrix payload: %v", err)
		}
	})

	http.Handle("/get", authGuard(getHandler))
	http.Handle("/set", authGuard(setHandler))
	http.Handle("/get_flags", authGuard(getFlagsHandler))

	log.Printf("Flagship Engine online [%s mode]. Control port listening on :8080...", cfg.AppEnv)
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalf("Server panic: %v", err)
	}
}
