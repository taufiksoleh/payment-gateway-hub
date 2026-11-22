# payment-gateway-hub

[![CI](https://github.com/taufiksoleh/payment-gateway-hub/actions/workflows/ci.yml/badge.svg)](https://github.com/taufiksoleh/payment-gateway-hub/actions/workflows/ci.yml)

One API for Indonesian payment gateways. Merchants sign up with the vendors
they want (**Midtrans**, **Xendit**, **DOKU**), give the hub their keys, and then
create payments, receive status updates and refund through a single,
consistent interface.

The hub never holds money. Funds settle directly from the vendor to the
merchant's own vendor account.

```
 Merchant app ──► payment-gateway-hub ──► Midtrans / Xendit / DOKU
      ▲                    │  ▲                    │
      └── signed webhook ──┘  └── vendor webhook ──┘
```

## Features

- Unified payment model: `virtual_account`, `qris`, `ewallet`, `retail_outlet`, `card`
- Unified status: `pending → paid / failed / expired / cancelled → partially_refunded / refunded`
- Merchants bring their own vendor accounts. Credentials are AES-256-GCM encrypted at rest
- Verified vendor webhooks (Midtrans SHA-512, Xendit callback token, DOKU HMAC-SHA256)
- Signed outbound webhooks to merchants (`X-Hub-Signature`), delivered through a transactional outbox: never lost on a crash, retried with exponential backoff
- Idempotent payment creation per `order_id`
- Optimistic locking on payments: concurrent webhooks, syncs and refunds never overwrite each other
- Double-refund protection: the refund amount is reserved before the vendor is called
- `Idempotency-Key` header on payment and refund creation, so client retries never charge or refund twice
- Rate limiting per merchant, per client IP and on admin endpoints, with standard `RateLimit-*` headers
- Plain Go: standard library router, `database/sql`, `log/slog`. One external dependency (pgx)

## Supported channels

| Method | Midtrans | Xendit | DOKU (hosted checkout) |
|---|---|---|---|
| `virtual_account` | bca, bni, bri, cimb, permata | bca, bni, bri, mandiri, permata, bsi, cimb | bca, mandiri, bri, bni, permata, cimb |
| `qris` | qris | qris | qris |
| `ewallet` | gopay, shopeepay | ovo, dana, shopeepay, linkaja | ovo, dana, shopeepay, linkaja |
| `retail_outlet` | alfamart, indomaret | alfamart, indomaret | alfamart, indomaret |
| `card` | — | — | card |
| Refund API | ✅ | ✅ | — (dashboard) |

## Quick start

Requirements: Go 1.25+, Docker (optional, for PostgreSQL).

```bash
cp .env.example .env
```

Fill `ENCRYPTION_KEY` (`openssl rand -base64 32`) and `ADMIN_TOKEN` (`openssl rand -hex 32`) in `.env`, then:

```bash
make db-up
```

```bash
make run
```

Leave `DATABASE_URL` empty to run with in-memory storage instead (no Docker needed).

### Try it

```bash
# 1. Register a merchant (admin). Save api_key and webhook_secret: shown once.
curl -s -X POST localhost:8080/v1/merchants \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"name":"Toko Budi","webhook_url":"https://toko.example/payment-hook"}'

# 2. Merchant connects its own Midtrans sandbox account.
curl -s -X PUT localhost:8080/v1/gateways/midtrans \
  -H "Authorization: Bearer $API_KEY" \
  -d '{"environment":"sandbox","secrets":{"server_key":"SB-Mid-server-..."}}'
#    → response includes "webhook_path": set https://<your-hub><webhook_path>
#      as the Payment Notification URL in the Midtrans dashboard.

# 3. Create a payment.
curl -s -X POST localhost:8080/v1/payments \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "order_id": "INV-2026-0001",
    "provider": "midtrans",
    "method": "virtual_account",
    "channel": "bca",
    "amount": 150000,
    "customer": {"name": "Budi", "email": "budi@example.com"},
    "expires_at": "2026-12-01T10:00:00+07:00"
  }'
#    → "action": {"virtual_account_number": "..."}  show this to the customer

# 4. Check status (or wait for your webhook).
curl -s localhost:8080/v1/payments/pay_... -H "Authorization: Bearer $API_KEY"
```

## API

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/healthz` | — | Liveness |
| GET | `/v1/providers` | — | Registered providers |
| POST | `/v1/merchants` | admin token | Register a merchant |
| GET | `/v1/gateways` | API key | List connected vendors |
| PUT | `/v1/gateways/{provider}` | API key | Add or replace vendor credentials |
| DELETE | `/v1/gateways/{provider}` | API key | Remove vendor credentials |
| POST | `/v1/payments` | API key | Create a payment |
| GET | `/v1/payments` | API key | Your payments, newest first (filters and paging below) |
| GET | `/v1/payments/{id}` | API key | Get a payment |
| POST | `/v1/payments/{id}/sync` | API key | Pull latest status from the vendor |
| POST | `/v1/payments/{id}/refunds` | API key | Refund (`amount` optional, defaults to full) |
| GET | `/v1/webhooks` | API key | Your webhooks, newest first (`status`, `limit`, `cursor`) |
| GET | `/v1/webhooks/{id}` | API key | One webhook, including its exact payload |
| POST | `/v1/webhooks/{id}/replay` | API key | Re-send a dead webhook |
| POST | `/v1/webhooks/replay` | API key | Re-send all your dead webhooks |
| POST | `/webhooks/{provider}/{merchant_id}` | vendor signature | Vendor notifications |

Errors always look like `{"error": {"code": "...", "message": "...", "field": "..."}}`.

### Listing payments

```bash
curl -s "localhost:8080/v1/payments?status=paid&provider=midtrans&created_from=2026-11-01T00:00:00%2B07:00&limit=50" \
  -H "Authorization: Bearer $API_KEY"
#   → {"payments":[{...}, ...], "next_cursor":"pay_..."}   ← pass as ?cursor= for the next page
```

| Parameter | Meaning |
|---|---|
| `status` | `pending`, `paid`, `failed`, `expired`, `cancelled`, `partially_refunded` or `refunded` |
| `provider` | `midtrans`, `xendit` or `doku` |
| `method` | `virtual_account`, `qris`, `ewallet`, `retail_outlet` or `card` |
| `order_id` | Your own order reference (exact match) |
| `created_from`, `created_to` | RFC 3339 times. `from` is inclusive, `to` is exclusive. Encode `+` as `%2B`, or use `Z` |
| `limit` | 1–200, default 50 |
| `cursor` | The `next_cursor` from the previous page. It is absent on the last page |

Filters combine with AND. Pages are cut by `(created_at, id)`, so new payments
arriving while you page through never cause skipped or repeated rows.

### Safe retries with `Idempotency-Key`

`POST /v1/payments` and `POST /v1/payments/{id}/refunds` accept an
`Idempotency-Key` header: any unique string up to 255 printable characters,
such as a UUID. Generate one per operation and reuse it when you retry that
same operation, for example after a timeout.

```bash
curl -s -X POST localhost:8080/v1/payments/pay_.../refunds \
  -H "Authorization: Bearer $API_KEY" \
  -H "Idempotency-Key: 6f1c2a0e-refund-INV-2026-0001" \
  -d '{"amount": 50000, "reason": "customer request"}'
```

| Situation | Response |
|---|---|
| First request with this key | Processed normally |
| Same key and body, first one succeeded | The stored response, with the header `Idempotent-Replayed: true` |
| Same key and body, first one still running | `409 idempotency_in_progress`. Retry shortly |
| Same key, different body or endpoint | `422 idempotency_key_reused` |
| Same key, first one failed (non-2xx) | Processed again, because failures are not stored |

Keys belong to one merchant and are remembered for 24 hours.

### Vendor credentials

| Provider | `secrets` keys |
|---|---|
| `midtrans` | `server_key` |
| `xendit` | `secret_key`, `callback_token` |
| `doku` | `client_id`, `secret_key` |

### Verifying hub webhooks (merchant side)

Each webhook carries `X-Hub-Signature: t=<unix>,v1=<hex>`. To verify it:

1. Compute `HMAC-SHA256(webhook_secret, t + "." + raw_body)` and compare it with `v1` using a constant-time comparison.
2. Reject any request where `t` is more than about 5 minutes old.

Event body: `{"id","type":"payment.paid","created_at","data":{...payment}}`.
`data` is the payment as it was right after the change.

**Delivery guarantees:**
- **At least once.** The same event can arrive more than once (for example if your endpoint answers slowly), so de-duplicate on `id`. Retries carry the same `id` and body.
- **Retries:** any non-2xx answer or timeout (10 seconds) is retried with exponential backoff: 10s, 20s, 40s and so on, capped at 1 hour. After 12 attempts (about 5 hours) the event is marked dead and kept for inspection in `webhook_outbox`.
- **Ordering is not guaranteed** while retries are happening. Use `data.updated_at` or `created_at` to order events, or re-fetch the payment.
- **No webhook URL:** if your merchant account has no `webhook_url`, events are settled without being sent.

### Inspecting and replaying webhooks

If your endpoint was down long enough for webhooks to die, fix it, then replay them:

```bash
# What died, and why?
curl -s "localhost:8080/v1/webhooks?status=dead" -H "Authorization: Bearer $API_KEY"
#   → {"webhooks":[{"id":"evt_...","type":"payment.paid","payment_id":"pay_...",
#        "status":"dead","attempts":12,"last_error":"merchant webhook answered http 500",...}],
#      "next_cursor":"evt_..."}      ← pass as ?cursor= for the next page

# Re-send one, or all of them at once.
curl -s -X POST localhost:8080/v1/webhooks/evt_.../replay -H "Authorization: Bearer $API_KEY"
curl -s -X POST localhost:8080/v1/webhooks/replay        -H "Authorization: Bearer $API_KEY"   # → {"replayed": 3}
```

- **Filters:** `status` is `pending`, `delivered` or `dead`. Leave it out to list everything. `limit` is 1–200 and defaults to 50.
- **What replay does:** it queues the webhook again within about a second, with a fresh 12 attempts and the **same** event `id` and payload.
- **Only dead webhooks can be replayed.** Anything else gets `409 invalid_state`. A delivered event would carry an id you have already processed.

### Rate limits

| Limit | Applies to | Default | Env var |
|---|---|---|---|
| Per merchant | Every authenticated `/v1` request | 600/min | `RATE_LIMIT_MERCHANT_PER_MIN` |
| Per client IP | Every `/v1` request, checked **before** authentication | 1200/min | `RATE_LIMIT_IP_PER_MIN` |
| Admin | `POST /v1/merchants`, per client IP (failed token guesses count) | 10/min | `RATE_LIMIT_ADMIN_PER_MIN` |

- **Bursts:** about 10 seconds' worth of requests can arrive at once (rate ÷ 6, minimum 5). Set a limit to `0` to disable it.
- **Never limited:** vendor webhooks (`/webhooks/...`), because vendors share IPs across all merchants and throttling them would delay payment updates. Also `/healthz`.
- **Headers:** every limited response carries `RateLimit-Limit`, `RateLimit-Remaining` and `RateLimit-Reset` (seconds until the bucket is full). Over the limit you get `429 rate_limited` with `Retry-After` in seconds.
- **Behind a load balancer:** set `TRUST_PROXY=true` so client IPs are read from `X-Forwarded-For` (the last entry, which your proxy appends). Leave it off otherwise, or clients could forge the header to dodge the limit.

## Project layout

```
cmd/hub/                 main: config → app.New → Run
internal/
  domain/                entities, value objects, business rules (no dependencies)
  usecase/               application services + ports (interfaces they need)
  gateway/               Gateway port, registry, shared HTTP helper
    midtrans/ xendit/ doku/   one adapter per vendor
    gatewaytest/         fake gateway for tests
  storage/
    postgres/            database/sql repositories + migrator
    memory/              in-memory repositories (tests, local dev)
  transport/
    httpapi/             REST handlers, middleware, error mapping
    dto/                 public JSON shapes (API + outbound webhooks)
  outbox/                transactional outbox: message, store port, retry policy (+ contract test)
  notify/                event encoding, signing, and the dispatcher that delivers the outbox
  idempotency/           Idempotency-Key record + store port (+ contract test)
  platform/              small utilities: id generation, encryption, rate limiting
  config/                environment variables
  app/                   composition root: wires everything
migrations/              SQL files, embedded and applied on startup
docs/                    architecture and how-to guides
```

Further reading:

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md): layers, the dependency rule, SOLID in this codebase, and request flows
- [docs/ADDING_A_GATEWAY.md](docs/ADDING_A_GATEWAY.md): a step-by-step guide to adding a vendor by hand

## Development

```bash
make test        # unit tests
make test-race   # with race detector
make lint        # golangci-lint
make cover       # coverage summary
```

PostgreSQL integration test:

```bash
make db-up
```

```bash
TEST_DATABASE_URL='postgres://hub:hub@localhost:5432/hub?sslmode=disable' go test ./internal/storage/postgres/
```

## Status and known limitations

This is a solid foundation, not yet production-certified. Before going live:

- **Verify each adapter against the vendor sandbox.** The request and response shapes follow vendor documentation and are covered by tests against recorded-style responses. They have not yet been run against live sandbox accounts.
- **Xendit refunds** are asynchronous on Xendit's side. The hub marks a refund done once Xendit accepts it.
- **Refund reconciliation:** if the process crashes between the vendor accepting a refund and the hub recording it, the amount stays in `pending_refund_amount`. It can't be refunded twice, but it needs a manual check against the vendor dashboard.
- **Retries without `Idempotency-Key`:** the header is optional. A client that retries a refund *without* it can refund again, up to the amount still refundable. Two simultaneous `POST /v1/payments` with the same `order_id` and no key can both reach the vendor before the database rejects the second one.
- **Midtrans cards** need client-side tokenization (Snap/JS) and are not supported. The Midtrans Mandiri VA (`echannel`) is not mapped yet.
- **Rate limits are per instance.** Buckets live in memory, so N hub instances allow up to N times each limit. Implement `ratelimit.Limiter` on a shared store (such as Redis) if you need exact global limits.
- **Not yet built:** API key rotation, metrics.
