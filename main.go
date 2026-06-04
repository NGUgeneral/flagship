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
	"flagship/docs"
	"flagship/middleware"

	_ "flagship/docs" // Dynamically generated package by 'swag init'

	"github.com/redis/go-redis/v9"
	httpSwagger "github.com/swaggo/http-swagger/v2"
)

var ctx = context.Background()

type Engine struct {
	mu           sync.RWMutex
	flags        map[string]bool
	rdb          *redis.Client
	redisHashKey string
}

func NewEngine(cfg *config.Config) *Engine {
	var opt *redis.Options
	var err error

	if cfg.RedisURL != "" {
		opt, err = redis.ParseURL(cfg.RedisURL)
		if err != nil {
			log.Fatalf("CRITICAL: Failed to parse secure Redis connection URL: %v", err)
		}
		log.Println("🔒 Redis client initialized securely via connection URL string (TLS Enabled).")
	} else {
		opt = &redis.Options{
			Addr:     cfg.RedisAddr,
			Password: cfg.RedisPassword,
			DB:       0,
		}
		log.Printf("🔓 Redis client initialized via unencrypted parameters. Target: %s", cfg.RedisAddr)
	}

	rdb := redis.NewClient(opt)

	engine := &Engine{
		flags:        make(map[string]bool),
		rdb:          rdb,
		redisHashKey: cfg.RedisHashKey,
	}

	engine.hydrateFromRedis()
	return engine
}

func (e *Engine) hydrateFromRedis() {
	e.mu.Lock()
	defer e.mu.Unlock()

	storedFlags, err := e.rdb.HGetAll(ctx, e.redisHashKey).Result()
	if err != nil {
		log.Printf("Warning: Failed to hydrate from Redis, starting fresh: %v", err)
		return
	}

	for k, v := range storedFlags {
		e.flags[k] = (v == "true")
	}
	log.Printf("Successfully hydrated %d flags into memory cache using namespace [%s].", len(e.flags), e.redisHashKey)
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

	if err := e.rdb.HSet(ctx, e.redisHashKey, compositeKey, valStr).Err(); err != nil {
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
	Service string `json:"service" example:"billing-service"`
	Key     string `json:"key" example:"enable-stripe-v2"`
	Value   bool   `json:"value" example:"true"`
}

type HealthResponse struct {
	Status string `json:"status" example:"healthy"`
	Redis  string `json:"redis" example:"connected"`
}

type ErrorResponse struct {
	Error string `json:"error" example:"Missing required parameters"`
}

// Global runtime metadata configuration block
// @title                      Flagship Feature Engine API
// @version                    1.0
// @description                High-performance inline feature flagging control plane.
// @host                       localhost:8080
// @BasePath                   /
// @securityDefinitions.apiKey  BearerAuth
// @in                         header
// @name                       Authorization
// @description                Type 'Bearer <your_jwt_token>' to access protected routes.
func main() {
	cfg := config.LoadConfig()
	engine := NewEngine(cfg)

	secretBytes := []byte(cfg.JwtSecretKey)
	authGuard := middleware.AuthMiddleware(secretBytes, cfg.JwtAlgorithm)

	// --- PUBLIC ROUTING ---
	http.HandleFunc("/health", handleHealth(engine))

	// --- AUTOMATED INTERACTIVE DOCUMENTATION TESTBENCH ---
	docs.SwaggerInfo.Host = cfg.AppHost
	http.Handle("/docs/", httpSwagger.Handler(
		httpSwagger.URL("/docs/doc.json"),
	))
	http.HandleFunc("/docs", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs/", http.StatusMovedPermanently)
	})

	// --- PROTECTED ROUTING ---
	http.Handle("/get", authGuard(http.HandlerFunc(handleGetFlag(engine))))
	http.Handle("/set", authGuard(http.HandlerFunc(handleSetFlag(engine))))
	http.Handle("/get_flags", authGuard(http.HandlerFunc(handleGetFlagsByService(engine))))

	log.Printf("Flagship Engine online [%s mode]. Control port listening on :8080...", cfg.AppEnv)
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalf("Server panic: %v", err)
	}
}

// --- EXTRACTED HANDLER LAYER CONTEXTS WITH SWAGGER TAGS ---

// handleHealth godoc
// @Summary      Engine Health Check
// @Description  Verifies running web engine operations and synchronous underlying upstash storage ping telemetry.
// @Tags         System
// @Produce      json
// @Success      200  {object}  HealthResponse
// @Failure      503  {object}  HealthResponse  "Service Unavailable - Storage cluster connection broken"
// @Router       /health [get]
func handleHealth(engine *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := engine.CheckRedisConnectivity(); err != nil {
			log.Printf("Health check failure: Redis unreachable: %v", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(HealthResponse{Status: "unhealthy", Redis: "disconnected"})
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(HealthResponse{Status: "healthy", Redis: "connected"})
	}
}

// handleGetFlag godoc
// @Summary      Get Individual Feature Flag Evaluation
// @Description  Evaluates a single state status targeting a specific composite namespace binding key.
// @Tags         Evaluation
// @Produce      plain
// @Param        service  query     string  true  "Target identifying domain service space"
// @Param        key      query     string  true  "The distinct targeting feature gate identifier identifier"
// @Security     BearerAuth
// @Success      200      {string}  string  "Service [billing] Flag [enable-v2]: true"
// @Failure      400      {string}  string  "Missing required parameters"
// @Failure      401      {string}  string  "Unauthorized missing payload token signature"
// @Router       /get [get]
func handleGetFlag(engine *Engine) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		service := r.URL.Query().Get("service")
		key := r.URL.Query().Get("key")

		if service == "" || key == "" {
			http.Error(w, "Missing required 'service' or 'key' parameter", http.StatusBadRequest)
			return
		}

		enabled := engine.GetFlag(service, key)
		fmt.Fprintf(w, "Service [%s] Flag [%s]: %t\n", service, key, enabled)
	}
}

// handleSetFlag godoc
// @Summary      Mutate/Set Target Feature State
// @Description  Commits an administrative state change parameter downward to secure cluster storage and instantly syncs internal tracking memory cache state.
// @Tags         Administration
// @Accept       json
// @Produce      plain
// @Param        payload  body      FlagPayload  true  "Target state runtime mutation specification matrix description block"
// @Security     BearerAuth
// @Success      200      {string}  string       "Successfully updated flag [billing:enable-v2] to true"
// @Failure      400      {string}  string       "Invalid configuration format parameters"
// @Failure      401      {string}  string       "Unauthorized"
// @Failure      500      {string}  string       "Internal persistence storage communication error context"
// @Router       /set [post]
func handleSetFlag(engine *Engine) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
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
	}
}

// handleGetFlagsByService godoc
// @Summary      Get All Component Configuration Flags for a Service Namespace
// @Description  Extracts the complete underlying operational state dataset block bounded to a single contextual microservice domain.
// @Tags         Evaluation
// @Produce      json
// @Param        service  query     string         true  "Target identifying domain service space context parameter mapping string"
// @Security     BearerAuth
// @Success      200      {object}  map[string]bool  "Example output dictionary block payload"
// @Failure      400      {object}  ErrorResponse
// @Failure      401      {string}  string           "Unauthorized"
// @Router       /get_flags [get]
func handleGetFlagsByService(engine *Engine) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		service := r.URL.Query().Get("service")
		if service == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(ErrorResponse{Error: "Missing required 'service' parameter"})
			return
		}

		flagsMatrix := engine.GetFlagsByService(service)

		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(flagsMatrix); err != nil {
			log.Printf("Failed to encode flags matrix payload: %v", err)
		}
	}
}
