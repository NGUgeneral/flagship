# Flagship Core Engine (`flagship`)

Flagship Core is a high-concurrency, lightning-fast feature flag evaluation engine written in Go. It operates on a low-latency cache model optimized for critical edge lookups.

## Data Strategy: Write-Through Cache & Hydration
To eliminate database latency bottlenecks on high-volume evaluation requests, the core engine splits its data paths:
* **The Source of Truth:** An isolated PostgreSQL instance tracks configuration schemas and transaction histories.
* **The Performance Layer:** A standalone Redis cache services execution queries in sub-milliseconds.
* **Lifecycle Flow:** On application cold start, a pipeline routine hydrates Redis entirely from PostgreSQL. Any subsequent administrative write operation performs an explicit Write-Through pattern: committing to SQL first, and updating/evicting the Redis cache block strictly upon SQL transaction success.

## API Routing Contract (v0.1)

All application routes are natively bound to the `/api/v1/` prefix.

### Public Client Endpoints (Proxied via Ingress)
* **`POST /api/v1/set`** — Administrative flag mutation. Commits payload variables to PostgreSQL and overwrites cache.
* **`POST /api/v1/get`** — Evaluates a feature flag state. Evaluates strictly out of Redis memory. *Intercepted natively by the upstream rate-limiter guard middleware.*

### Diagnostics
* **`GET /health`** — Performs deep connectivity assertions against downstream Redis and PostgreSQL clusters. (Internal container scope only).

## Environment Configuration

The engine relies entirely on runtime environment variable injection:
* `APP_ENV`: Deployment runtime state (`development` / `production`).
* `DB_HOST` / `DB_PORT` / `DB_USER` / `DB_PASSWORD` / `DB_NAME`: PostgreSQL transaction cluster configurations.
* `REDIS_ADDR`: Internal target network string for cache storage.
* `JWT_SECRET_KEY`: High-entropy symmetric key used to parse and authenticate incoming client authorization contexts.
* `RATE_LIMITER_URL`: Internal destination loopback address (`http://rate-limiter:8000/api/v1/is_allowed`) utilized to verify active consumer throttling frames out-of-band.

## Local Development Setup

Please refer to [Flagship-Platform](https://github.com/NGUgeneral/flagship-platform) for detailed local development setup instructions.
