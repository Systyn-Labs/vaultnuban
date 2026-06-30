# VaultNUBAN

[![CI](https://github.com/systynlabs/vaultnuban/actions/workflows/ci.yml/badge.svg)](https://github.com/systynlabs/vaultnuban/actions/workflows/ci.yml)

Multi-tenant dedicated virtual account (DVA) infrastructure built on the [Nomba Virtual Accounts API](https://developer.nomba.com). Built for the **DevCareer × Nomba Hackathon — Infrastructure Track**.

Each tenant onboards customers, provisions a unique 10-digit NUBAN for each one, and receives inbound transfers into a double-entry ledger — with webhook relay, withdrawals, collections, suspense management, KYC-tier enforcement, and sweep-based reconciliation.

**Live links**

| Surface | URL |
|---------|-----|
| API | https://vaultnuban.onrender.com |
| Dashboard | https://vaultnuban-client.pages.dev |
| Docs | https://vaultnuban-docs.pages.dev |
| Dashboard repo | https://github.com/kellslte/vaultnuban-client |
| Docs repo | https://github.com/Systyn-Labs/vaultnuban-docs |

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
- [Withdrawals](#withdrawals)
- [Collections](#collections)
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
          └──→ Tenant relay endpoints
               fan-out signed HTTP POST to registered URLs
```

A **sweep runner** polls the Nomba Transactions API on a configurable interval to catch any payments the webhook ingestor missed. Both paths share the same idempotent `PostTransaction` function, so double-posting is structurally impossible.

> **Note:** VA inbound NIP credits are delivered by Nomba via webhook only — they do not appear in the Transactions API. The sweep acts as a backstop for other transaction types and keeps the free-tier instance warm.

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
| **Logging** | Structured console output with ANSI colours, TTY detection, and log-level alignment. |

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
│   │   │                #   suspense, relay, webhook, sweep, withdrawals, collections
│   │   ├── middleware/  # Auth (API key hash), idempotency (Redis), request logger
│   │   └── problem/     # RFC 9457 problem detail helpers
│   ├── config/          # Typed env-var loading
│   ├── domain/          # Pure entity types — no I/O
│   ├── ledger/          # Only place that constructs LedgerEntry slices
│   ├── logger/          # Structured logger
│   ├── provider/
│   │   ├── nomba/       # Nomba API client (OAuth2, virtual accounts, transactions,
│   │   │                #   webhooks, transfers v2, account resolution)
│   │   └── fakeprov/    # In-memory provider for harness tests
│   ├── recon/
│   │   ├── matcher.go   # NUBAN → customer resolution, tier-limit checks
│   │   ├── worker.go    # Async credit/reversal processor + ProcessDirect for sweep
│   │   └── sweep.go     # Nomba Transactions API poller
│   ├── relay/           # Tenant webhook fan-out (HMAC-signed, retry with back-off)
│   ├── service/         # Business logic: customer, provisioning, suspense,
│   │                    #   withdrawals, collections
│   └── store/
│       ├── store.go     # Repository interfaces
│       ├── db.go        # pgxpool + custom migration runner
│       ├── memstore/    # In-memory store implementations (harness / unit tests)
│       ├── migrations/  # Numbered SQL migrations (up + down)
│       └── postgres/    # pgx implementations of every store interface
├── .env.example
├── Dockerfile
├── Makefile
└── render.yaml          # Render Blueprint for one-click deploy
```

---

## Getting started

### Prerequisites

- Go 1.22+
- PostgreSQL 15+ (or a [Neon](https://neon.tech) free project)
- Redis 7+ (or an [Upstash](https://upstash.com) free database)
- A [Nomba developer account](https://developer.nomba.com) with a configured app and sub-account ID

### Local setup

```bash
# 1. Clone the repo
git clone https://github.com/Systyn-Labs/vaultnuban.git
cd vaultnuban

# 2. Copy and fill in environment variables
cp .env.example .env
# Edit .env with your Nomba credentials, DATABASE_URL, and REDIS_URL

# 3. Build and run
make run
# The server starts on :32091 and applies all migrations automatically on boot.

# 4. Verify
curl http://localhost:32091/healthz
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
| `NOMBA_SUB_ACCOUNT_ID` | ✅ | — | Sub-account ID for VA creation and transaction queries |
| `NOMBA_BASE_URL` | ✅ | — | `https://api.nomba.com` (production) or `https://sandbox.nomba.com` |
| `NOMBA_WEBHOOK_SECRET` | ✅ | — | Webhook signing secret |
| `DATABASE_URL` | ✅ | — | PostgreSQL connection string (e.g. Neon pooled URL) |
| `REDIS_URL` | ✅ | — | Redis URL (`rediss://` for TLS, e.g. Upstash) |
| `INTERNAL_SWEEP_TOKEN` | ✅ | — | Bearer token for `POST /internal/sweep` |
| `SWEEP_INTERVAL` | | `10m` | How often the sweep runs |
| `SWEEP_OVERLAP` | | `15m` | Overlap window to catch late-arriving events |
| `TIER_LIMITS_JSON` | | CBN defaults | JSON map of KYC tier → daily/balance caps (kobo) |
| `PORT` | | `32091` | HTTP listen port |
| `ENV` | | `development` | Runtime environment label |

See [`.env.example`](.env.example) for a fully annotated template.

---

## API reference

Full documentation is available at **https://vaultnuban-docs.pages.dev**.

A [Bruno collection](docs/bruno/) and Postman collection are also provided with pre-wired environments for local and production. Post-response scripts automatically capture IDs into collection variables so requests can be run in sequence.

### Endpoint map

**Tenant API** — `Authorization: Bearer <api_key>` required

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/healthz` | Liveness check — no auth |
| `POST` | `/v1/customers` | Create a customer |
| `GET` | `/v1/customers` | List customers (cursor-paginated) |
| `PATCH` | `/v1/customers/{id}/identity` | Update KYC tier |
| `GET` | `/v1/customers/{id}/balance` | Computed balance |
| `POST` | `/v1/customers/{id}/virtual-account` | Provision a NUBAN |
| `GET` | `/v1/customers/{id}/virtual-account` | Get virtual account details |
| `PATCH` | `/v1/customers/{id}/virtual-account` | Rename / suspend / unsuspend |
| `DELETE` | `/v1/customers/{id}/virtual-account` | Close (irreversible) — 204 |
| `GET` | `/v1/virtual-accounts` | All VAs for tenant |
| `GET` | `/v1/transactions` | Tenant-wide transactions (cursor-paginated) |
| `GET` | `/v1/customers/{id}/statement` | Account statement with running balance |
| `GET` | `/v1/suspense` | List open suspense items |
| `PATCH` | `/v1/suspense/{id}` | Resolve (reassign or refund_flagged) |
| `GET` | `/v1/payees/resolve` | Resolve bank account name before withdrawal |
| `POST` | `/v1/customers/{id}/withdrawals` | Initiate outbound bank transfer |
| `GET` | `/v1/customers/{id}/withdrawals` | List withdrawals |
| `POST` | `/v1/customers/{id}/collections` | Create a payment collection request |
| `GET` | `/v1/customers/{id}/collections` | List collections |
| `GET` | `/v1/customers/{id}/collections/{cid}` | Get collection |
| `DELETE` | `/v1/customers/{id}/collections/{cid}` | Cancel collection — 204 |
| `POST` | `/v1/webhook-endpoints` | Register a relay URL |
| `GET` | `/v1/webhook-endpoints` | List relay endpoints |
| `GET` | `/v1/webhook-deliveries` | List relay delivery attempts |
| `POST` | `/v1/webhook-deliveries/{id}/replays` | Manual replay |
| `GET` | `/v1/api-keys` | List API keys |
| `POST` | `/v1/api-keys` | Create API key |
| `DELETE` | `/v1/api-keys/{id}` | Revoke API key |
| `GET` | `/v1/audit` | Audit log |

**Ingest & internal**

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `POST` | `/webhooks/nomba` | HMAC (Nomba) | Payment event ingestor |
| `POST` | `/internal/sweep` | sweep token | Trigger reconciliation sweep |
| `GET` | `/internal/health` | admin token | Global platform health (Σ DR = Σ CR) |
| `GET` | `/internal/sweep-runs` | admin token | Sweep run log |
| `GET` | `/internal/tenants` | admin token | List tenants |
| `POST` | `/internal/tenants` | admin token | Onboard tenant |
| `GET` | `/internal/suspense` | admin token | Cross-tenant suspense |
| `GET` | `/internal/virtual-accounts` | admin token | All VAs across tenants |
| `GET` | `/internal/nomba-virtual-accounts` | admin token | Nomba VA list |
| `GET` | `/internal/webhook-events` | admin token | Webhook event log |

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
4. Worker processes: match NUBAN → customer, check tier limits, post balanced ledger entries

**Sweep path (recovery)**
- `POST /internal/sweep` polls `POST /v1/transactions/accounts/{subAccountId}` with `{"type":"transfer"}` from `lastSweepTime − overlap` to `now`
- Transactions already posted by the webhook are skipped (idempotent by `transactionId` primary key)
- Run result is persisted to `sweep_runs` and returned in the response body

> **Important:** VA inbound NIP credits only arrive via webhook — they do not appear in Nomba's Transactions API. The sweep covers other transaction types. Register `/webhooks/nomba` with Nomba production to ensure VA credit durability.

Configure UptimeRobot (or any HTTP monitor) to POST to `/internal/sweep` every 5 minutes with `Authorization: Bearer <INTERNAL_SWEEP_TOKEN>` — this both keeps the Render free-tier instance alive and drives the sweep.

---

## Suspense management

Credits that cannot be cleanly posted to a customer wallet are routed to the suspense ledger account. Reasons:

| Reason | Trigger |
|--------|---------|
| `unmatched` | NUBAN not found in any virtual account |
| `closed_account` | NUBAN belongs to a CLOSED virtual account |
| `suspended_account` | NUBAN belongs to a SUSPENDED virtual account |
| `tier_limit` | Credit would breach the customer's KYC-tier daily cap or max balance |

Suspense items are resolved via `PATCH /v1/suspense/{id}`:

- **`reassign`** — posts compensating ledger entries (DR suspense / CR customer_wallet). Requires `target_customer_id`.
- **`refund_flagged`** — marks the item for manual bank reversal. Funds remain in suspense.

The `Σ debits = Σ credits` invariant holds across all paths including suspense resolution.

---

## Webhook relay

Tenants register one or more URLs to receive signed payment event notifications:

```http
POST /v1/webhook-endpoints
Authorization: Bearer <api_key>

{
  "url": "https://your-backend.example.com/webhooks/vaultnuban",
  "secret": "your-signing-secret"
}
```

Each delivery includes:

```
X-VaultNUBAN-Signature: sha256=<hmac-hex>
X-VaultNUBAN-Attempt: 1
Content-Type: application/json
```

Failed deliveries retry with exponential back-off before moving to `dead_letter`. Use `POST /v1/webhook-deliveries/{id}/replays` to manually replay any delivery.

---

## Withdrawals

Initiate an outbound bank transfer on behalf of a customer in two steps:

```bash
# 1. Resolve the destination account name first
GET /v1/payees/resolve?bank_code=999999&account_number=0123456789

# 2. Initiate the transfer
POST /v1/customers/{id}/withdrawals
{
  "amount_kobo": 500000,
  "destination_bank_code": "999999",
  "destination_account_number": "0123456789",
  "destination_account_name": "Amaka Osei",
  "narration": "Payout - July invoice"
}
```

Withdrawals are processed via Nomba's `POST /v2/transfers/bank/{subAccountId}`. Status lifecycle: `pending → processing → completed | failed`.

---

## Collections

Create a time-bound, optionally amount-constrained payment request tied to a customer's existing NUBAN:

```bash
POST /v1/customers/{id}/collections
{
  "reference": "INV-2026-001",
  "description": "July subscription",
  "expected_amount_kobo": 500000,
  "expires_in_seconds": 604800
}
```

The response includes the customer's NUBAN and bank name to share with the payer. When a matching inbound credit arrives, the collection status automatically moves to `fulfilled`. Statuses: `open → fulfilled | expired | cancelled`.

---

## KYC tier limits

Limits are configured via `TIER_LIMITS_JSON`. The default matches CBN regulations:

| Tier | Daily credit cap | Max balance |
|------|-----------------|-------------|
| 1 | ₦50,000 | ₦300,000 |
| 2 | ₦200,000 | ₦5,000,000 |
| 3 | Uncapped | Uncapped |

All amounts are stored and computed in kobo (₦1 = 100 kobo). A credit that would breach either limit is routed to suspense with reason `tier_limit`.

---

## Running tests

```bash
# Run all tests
make test

# Run only the reconciliation harness (verbose)
make harness
```

The harness (`harness/harness_test.go`) runs scenarios entirely in-memory — no database or network required:

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

Every scenario ends with an NFR-1 assertion: `Σ credits == Σ debits` across all ledger accounts.

---

## Deployment

### One-click deploy to Render

1. Fork or push this repo to GitHub
2. In the [Render dashboard](https://render.com), click **New → Blueprint** and connect the repo
3. Render detects `render.yaml` and creates the service automatically
4. Set the following env vars in **Environment**:
   - `NOMBA_CLIENT_ID`, `NOMBA_CLIENT_SECRET`, `NOMBA_ACCOUNT_ID`, `NOMBA_SUB_ACCOUNT_ID`
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
| Method | POST |
| Interval | 5 minutes |
| Custom header | `Authorization: Bearer <INTERNAL_SWEEP_TOKEN>` |

### Register the Nomba webhook

In the Nomba developer portal (or via API), set your webhook URL to:

```
https://vaultnuban.onrender.com/webhooks/nomba
```

This is the only mechanism by which VA inbound NIP credits are delivered. Ensure this endpoint is registered and reachable before any production testing.

---

## Design decisions

**SubAccountID as a first-class concept**
Nomba VA creation, transaction queries, and transfers all require a `subAccountId` as a path parameter (not a body field). `NOMBA_SUB_ACCOUNT_ID` is a required environment variable that scopes all VA operations to a specific sub-account, isolating our VAs from other teams' activity on the shared parent account.

**accountRef has a random suffix**
The format `t{8hex}c{20hex}{8hex_random}` ensures re-provisioning the same customer never collides on Nomba's side — Nomba sandbox does not honour VA deletion, so a purely deterministic ref would 400 on re-provision. The local `GetActiveVA` guard still prevents duplicate active VAs in our system.

**Why not hold funds long-term / use Nomba subaccounts as wallets?**
Nomba Virtual Accounts are routing identifiers — inbound transfers land in the tenant's Nomba parent account. VaultNUBAN's ledger is a liability ledger (accounting record), not a PSP-held float. This design matches the hackathon brief and Nomba's DVA product intent.

**Why a custom migration runner instead of golang-migrate?**
`golang-migrate/migrate/v4/database/postgres` pulls in Docker test dependencies whose `go.sum` checksums conflicted in the module graph. The custom runner is ~60 lines using `embed.FS` + pgx, applies migrations idempotently via a `schema_migrations` table, and has zero transitive dependencies.

**Why `ON CONFLICT DO NOTHING` instead of `ON CONFLICT DO UPDATE`?**
Webhook delivery and sweep processing race constantly. The natural idempotency key is Nomba's `transactionId` (the primary key on `transactions`). Silently ignoring the second insert is correct: the first writer wins, all subsequent attempts are no-ops.

**Why in-process goroutine fan-out for relay instead of a queue?**
Render free tier has no persistent worker process. An in-process goroutine fan-out is sufficient for the hackathon scope and keeps the deployment to a single binary. Failed deliveries are persisted to `relay_deliveries` and retryable via `POST /v1/webhook-deliveries/{id}/replays`.
