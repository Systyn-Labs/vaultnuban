# VaultNUBAN

[![CI](https://github.com/systynlabs/vaultnuban/actions/workflows/ci.yml/badge.svg)](https://github.com/systynlabs/vaultnuban/actions/workflows/ci.yml)

Multi-tenant dedicated virtual account (DVA) infrastructure built on the [Nomba Virtual Accounts API](https://developer.nomba.com). Built for the **DevCareer × Nomba Hackathon — Infrastructure Track**.

Each tenant onboards customers, provisions a unique 10-digit NUBAN for each one, and receives inbound transfers to a double-entry ledger — with webhook relay, suspense management, KYC-tier enforcement, and sweep-based reconciliation.

---

## Table of Contents

- [How it works](#how-it-works)
- [Architecture](#architecture)
- [Project structure](#project-structure)
- [Getting started](#getting-started)
- [Environment variables](#environment-variables)
- [API reference](#api-reference)
- [Reconciliation](#reconciliation)
- [Suspense management](#suspense-management)
- [Webhook relay](#webhook-relay)
- [KYC tier limits](#kyc-tier-limits)
- [Running tests](#running-tests)
- [Deployment](#deployment)
- [Design decisions](#design-decisions)

---

## How it works

```
Sender → Nomba → Virtual NUBAN
                      │
              ┌───────▼────────┐
              │  Webhook POST   │  HMAC-SHA256 verified, 5-min replay window
              │  /webhooks/nomba│  deduped by transactionId+eventType
              └───────┬────────┘
                      │ async (buffered channel, 512)
              ┌───────▼────────┐
              │  Recon Worker   │  match NUBAN → customer wallet
              │                 │  enforce KYC tier limits
              │                 │  post double-entry ledger
              └───────┬────────┘
               posted │ suspensed
          ┌────────────┴───────────┐
    customer_wallet            suspense
    (double-entry ledger)      (manual resolution)
          │
          └──→ Tenant relay endpoints (FR-11)
               fan-out signed HTTP POST to registered URLs
```

A **sweep runner** polls the Nomba Transactions API on a configurable interval to catch any payments the webhook ingestor missed. Both paths share the same `ProcessDirect` code and the same idempotency guard, so double-posting is impossible.

---

## Architecture

| Concern | Approach |
|---------|---------|
| **Framework** | [go-chi/chi v5](https://github.com/go-chi/chi) — lightweight, stdlib-compatible |
| **Database** | PostgreSQL via [pgx/v5](https://github.com/jackc/pgx) with a custom migration runner (no external migrate tool) |
| **Cache / idempotency** | Redis via [go-redis/v9](https://github.com/redis/go-redis) — SETNX-based idempotency keys (24 h TTL) |
| **Ledger** | Double-entry, append-only. `PostTransaction` is one DB transaction: INSERT transactions + INSERT ledger_entries. `Σ debits = Σ credits` enforced in code and asserted by the harness. |
| **Nomba auth** | Single-flight OAuth2 token cache with 28-min TTL. Concurrent requests block on one refresh; never floods Nomba with parallel auth calls. |
| **Async processing** | In-process buffered channel (512 items). If full, the webhook acks immediately and the sweep recovers. |
| **Multi-tenancy** | Every row is scoped by `tenant_id`. Cross-tenant lookups return 404, not 403. |
| **Error format** | [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) `application/problem+json` on all error responses. |
| **Logging** | NestJS-style console output with ANSI colours, TTY detection, and log-level alignment. |

---

## Project structure

```
vaultnuban/
├── cmd/server/          # main.go — wires all dependencies and starts the HTTP server
├── docs/
│   ├── openapi.yaml     # OpenAPI 3.1 specification
│   └── bruno/           # Bruno API collection (environments: local, production)
├── harness/             # Integration scenario tests (no DB required)
├── internal/
│   ├── api/
│   │   ├── handlers/    # HTTP handlers: customers, virtual_accounts, transactions,
│   │   │                #   suspense, relay, webhook, sweep
│   │   ├── middleware/  # Auth (API key hash), idempotency (Redis), request logger
│   │   └── problem/     # RFC 9457 problem detail helpers
│   ├── config/          # Typed env-var loading
│   ├── domain/          # Pure entity types — no I/O
│   ├── ledger/          # Only place that constructs LedgerEntry slices
│   ├── logger/          # NestJS-style structured logger
│   ├── provider/
│   │   ├── nomba/       # Nomba API client (OAuth2, virtual accounts, transactions, webhooks)
│   │   └── fakeprov/    # In-memory provider for harness tests
│   ├── recon/
│   │   ├── matcher.go   # NUBAN → customer resolution, tier-limit checks
│   │   ├── worker.go    # Async credit/reversal processor + ProcessDirect for sweep
│   │   └── sweep.go     # Nomba Transactions API poller
│   ├── relay/           # Tenant webhook fan-out (HMAC-signed, retry with back-off)
│   ├── service/         # Business logic: customer, provisioning, suspense
│   └── store/
│       ├── store.go     # Repository interfaces
│       ├── db.go        # pgxpool + custom migration runner
│       ├── memstore/    # In-memory store implementations (harness / unit tests)
│       ├── migrations/  # 015 numbered SQL migrations (up + down)
│       └── postgres/    # pgx implementations of every store interface
├── .env.example
├── Dockerfile
├── Makefile
└── render.yaml          # Render Blueprint for one-click deploy
```

---

## Getting started

### Prerequisites

- Go 1.26+
- PostgreSQL 15+ (or a [Neon](https://neon.tech) free project)
- Redis 7+ (or an [Upstash](https://upstash.com) free database)
- A [Nomba developer account](https://developer.nomba.com) with a configured app

### Local setup

```bash
# 1. Clone the repo
git clone https://github.com/your-org/vaultnuban.git
cd vaultnuban

# 2. Copy and fill in environment variables
cp .env.example .env
# Edit .env with your Nomba credentials, DATABASE_URL, and REDIS_URL

# 3. Build and run
make run
# The server starts on :8080 and applies all migrations automatically on boot.

# 4. Verify
curl http://localhost:8080/healthz
# {"status":"ok"}
```

### Docker

```bash
make docker-build
make docker-run   # reads .env file automatically
```

---

## Environment variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `NOMBA_CLIENT_ID` | ✅ | — | Nomba app client ID |
| `NOMBA_CLIENT_SECRET` | ✅ | — | Nomba app client secret |
| `NOMBA_ACCOUNT_ID` | ✅ | — | Your Nomba parent account ID |
| `NOMBA_BASE_URL` | ✅ | — | `https://api.nomba.com/v1` |
| `NOMBA_WEBHOOK_SECRET` | ✅ | — | Webhook signing secret from Nomba portal |
| `DATABASE_URL` | ✅ | — | PostgreSQL connection string (e.g. Neon pooled URL) |
| `REDIS_URL` | ✅ | — | Redis URL (`rediss://` for TLS, e.g. Upstash) |
| `INTERNAL_SWEEP_TOKEN` | ✅ | — | Bearer token for `GET /internal/sweep` |
| `SWEEP_INTERVAL` | | `10m` | How far back the first sweep looks |
| `SWEEP_OVERLAP` | | `15m` | Overlap window to catch late-arriving events |
| `TIER_LIMITS_JSON` | | CBN defaults | JSON map of KYC tier → daily/balance caps (kobo) |
| `PORT` | | `8080` | HTTP listen port (set automatically by Render) |
| `ENV` | | `development` | Runtime environment label |

See [`.env.example`](.env.example) for a fully annotated template.

---

## API reference

A full [OpenAPI 3.1 spec](docs/openapi.yaml) is included. Import it into any OpenAPI-compatible tool (Swagger UI, Stoplight, Redocly, etc.).

A [Bruno collection](docs/bruno/) is also provided with pre-wired environments for local and production. Post-response scripts automatically capture IDs into collection variables so requests can be run in sequence.

### Quick endpoint map

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/healthz` | Liveness check — no auth |
| `POST` | `/v1/customers` | Create (or retrieve) a customer |
| `PATCH` | `/v1/customers/{id}/identity` | Upgrade KYC tier |
| `POST` | `/v1/customers/{id}/virtual-account` | Provision a NUBAN |
| `GET` | `/v1/customers/{id}/virtual-account` | Get virtual account details |
| `PATCH` | `/v1/customers/{id}/virtual-account` | Rename / suspend / unsuspend |
| `DELETE` | `/v1/customers/{id}/virtual-account` | Close (irreversible) |
| `GET` | `/v1/customers/{id}/transactions` | List transactions (cursor pagination) |
| `GET` | `/v1/customers/{id}/statement` | Account statement with running balance |
| `GET` | `/v1/suspense` | List open suspense items |
| `POST` | `/v1/suspense/{itemID}/resolve` | Resolve (reassign or flag for refund) |
| `POST` | `/v1/webhook-endpoints` | Register a tenant relay URL |
| `POST` | `/webhooks/nomba` | Nomba payment event ingestor — HMAC verified |
| `GET/HEAD` | `/internal/sweep` | Trigger reconciliation sweep |

### Authentication

All `/v1/*` endpoints require:

```
Authorization: Bearer <api_key>
```

### Idempotency

Add `Idempotency-Key: <your-key>` to any mutating request. Replaying the same key within 24 hours returns the original response without re-executing the operation. Concurrent requests with the same key while the first is in-flight receive `409 Conflict`.

### Error format

All errors are `application/problem+json` (RFC 9457):

```json
{
  "type": "https://vaultnuban.systynlabs.com/problems/not-found",
  "title": "Not Found",
  "status": 404,
  "detail": "customer not found"
}
```

---

## Reconciliation

Payment events arrive via two paths that converge on the same idempotent `PostTransaction` function:

**Webhook path (real-time)**
1. Nomba POSTs to `/webhooks/nomba`
2. Signature verified (HMAC-SHA256), timestamp checked (5-min replay window), event deduped by `transactionId:eventType`
3. Acked `200` immediately; enqueued to in-process channel (512 buffer)
4. Worker processes: match NUBAN → customer, check tier limits, post ledger entries

**Sweep path (recovery)**
- `GET /internal/sweep` (or `HEAD` for UptimeRobot free plan) polls the Nomba Transactions API from `lastSweepTime − overlap` to `now`
- Transactions already posted by the webhook are skipped (idempotent by transactionId primary key)
- Sweep result is returned in the response body and logged to `sweep_runs`

Configure UptimeRobot (or any HTTP monitor) to hit `/internal/sweep` every 5 minutes with `Authorization: Bearer <INTERNAL_SWEEP_TOKEN>` — this both keeps the Render free-tier instance alive and drives the sweep.

---

## Suspense management

Credits that cannot be cleanly posted to a customer wallet are routed to the suspense ledger account instead. Reasons:

| Reason | Trigger |
|--------|---------|
| `unmatched` | NUBAN not found in any virtual account |
| `closed_account` | NUBAN belongs to a CLOSED virtual account |
| `suspended_account` | NUBAN belongs to a SUSPENDED virtual account |
| `tier_limit` | Credit would breach the customer's KYC-tier daily cap or max balance |

Suspense items are resolved via `POST /v1/suspense/{itemID}/resolve`:

- **`reassign`** — posts compensating ledger entries (DR suspense / CR customer_wallet) to transfer the funds to a specified customer. Requires `target_customer_id`.
- **`refund_flagged`** — marks the item for manual bank reversal. Funds remain in the suspense account. No ledger entries posted.

The double-entry invariant (Σ debits = Σ credits) is maintained across all paths including resolution.

---

## Webhook relay

Tenants can register one or more URLs to receive payment event notifications:

```http
POST /v1/webhook-endpoints
Authorization: Bearer <api_key>

{
  "url": "https://your-backend.example.com/webhooks/vaultnuban",
  "secret": "your-signing-secret"
}
```

When a credit is posted to a customer wallet, VaultNUBAN fans out a signed HTTP POST to all active relay endpoints for that tenant. Each request includes:

```
X-VaultNUBAN-Signature: sha256=<hmac-hex>
X-VaultNUBAN-Attempt: 1
Content-Type: application/json
```

Verify the signature on your end:
```
expectedSig = "sha256=" + HMAC-SHA256(requestBody, SHA256(yourSecret))
```

Failed deliveries are retried with exponential back-off (30 s → 5 min) before being moved to `dead_letter` after 3 attempts.

---

## KYC tier limits

Limits are configured via `TIER_LIMITS_JSON`. The default matches CBN regulations:

| Tier | Daily credit cap | Max balance |
|------|-----------------|-------------|
| 1 | ₦50,000 | ₦300,000 |
| 2 | ₦200,000 | ₦5,000,000 |
| 3 | Uncapped | Uncapped |

All amounts are stored and computed in kobo (₦1 = 100 kobo). A credit that would breach either limit is routed to suspense with reason `tier_limit` rather than being rejected — the funds are safe and can be reassigned once the customer's tier is upgraded.

---

## Running tests

```bash
# Run all tests
make test

# Run only the reconciliation harness (verbose)
make harness
```

The harness (`harness/harness_test.go`) runs 8 scenarios entirely in-memory — no database or network required:

| Scenario | What it proves |
|----------|---------------|
| I-01 | Webhook happy path — customer wallet credited |
| I-02 | Duplicate transactionId — idempotent, no double-credit |
| I-03 | Missed webhook — sweep recovery posts the transaction |
| I-04 | Closed account — credit routed to suspense |
| I-05 | Unknown NUBAN — credit routed to suspense (unmatched) |
| I-06 | Reversal — balance returns to zero after credit + reversal |
| I-07 | Tier-1 max-balance cap — overflow credit goes to suspense |
| I-08 | Sweep encounters already-posted txnId — skips it |

Every scenario ends with an NFR-1 assertion: `Σ credits == Σ debits` across all ledger accounts. Any imbalance fails the test immediately.

---

## Deployment

### One-click deploy to Render

1. Fork or push this repo to GitHub
2. In the [Render dashboard](https://render.com), click **New → Blueprint** and connect the repo
3. Render detects `render.yaml` and creates the service automatically
4. Set the following env vars manually in **Environment** (they are marked `sync: false` in the Blueprint):
   - `NOMBA_CLIENT_ID`
   - `NOMBA_CLIENT_SECRET`
   - `NOMBA_ACCOUNT_ID`
   - `NOMBA_WEBHOOK_SECRET`
   - `DATABASE_URL` — paste the **pooled** connection string from [Neon](https://neon.tech)
   - `REDIS_URL` — paste the `rediss://` URL from [Upstash](https://upstash.com)
5. `INTERNAL_SWEEP_TOKEN` is auto-generated by Render (`generateValue: true`)
6. Deploy — migrations run automatically on startup

### UptimeRobot monitor (keep-alive + sweep)

| Field | Value |
|-------|-------|
| Monitor type | HTTP(s) |
| URL | `https://vaultnuban.onrender.com/internal/sweep` |
| Method | **HEAD** (required for UptimeRobot free plan) |
| Interval | 5 minutes |
| Custom header | `Authorization: Bearer <INTERNAL_SWEEP_TOKEN>` |

This single monitor keeps the free-tier instance warm and drives the reconciliation sweep. The `HEAD` method triggers the sweep handler normally — chi runs it but discards the response body per the HTTP spec.

### Register the Nomba webhook

In the Nomba developer portal, set your webhook URL to:

```
https://vaultnuban.onrender.com/webhooks/nomba
```

---

## Design decisions

**Why not hold funds long-term / use Nomba subaccounts?**
Nomba Virtual Accounts are routing identifiers — inbound transfers land in the tenant's Nomba parent account, not in a segregated float. VaultNUBAN's ledger is a liability ledger (accounting record), not a PSP-held float. Subaccounts serve a different use case (segregated settlement pools). This design matches the hackathon brief and Nomba's DVA product.

**Why a custom migration runner instead of golang-migrate?**
`golang-migrate/migrate/v4/database/postgres` pulls in Docker test dependencies (`opencontainers/image-spec`) whose `go.sum` checksum conflicted in the module graph. The custom runner is ~60 lines using `embed.FS` + pgx, applies migrations idempotently via a `schema_migrations` table, and has zero transitive dependencies.

**Why `ON CONFLICT DO NOTHING` instead of `ON CONFLICT DO UPDATE`?**
Webhook delivery and sweep processing race constantly. The natural idempotency key is Nomba's `transactionId` (the primary key on `transactions`). Silently ignoring the second insert is correct: the first writer wins, all subsequent attempts are no-ops. No lost updates, no compare-and-swap complexity.

**Why fan-out relay in goroutines rather than a queue?**
Render free tier has no persistent worker process. An in-process goroutine fan-out is sufficient for the hackathon scope and keeps the deployment to a single binary. Failed deliveries are persisted to `relay_deliveries` and retried on the next sweep run, providing durability without a queue service.
