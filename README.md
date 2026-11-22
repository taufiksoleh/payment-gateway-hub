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
- Signed outbound webhooks to merchants (`X-Hub-Signature`), with retries
- Idempotent payment creation per `order_id`
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
| GET | `/v1/payments/{id}` | API key | Get a payment |
| POST | `/v1/payments/{id}/sync` | API key | Pull latest status from the vendor |
| POST | `/v1/payments/{id}/refunds` | API key | Refund (`amount` optional, defaults to full) |
| POST | `/webhooks/{provider}/{merchant_id}` | vendor signature | Vendor notifications |

Errors always look like `{"error": {"code": "...", "message": "...", "field": "..."}}`.

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
  notify/                outbound merchant webhooks (HTTP + async retry decorator)
  platform/              small utilities: id generation, encryption
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
- **Concurrent updates:** a webhook and a `/sync` racing on the same payment use last-write-wins. Add optimistic locking (a `version` column) if that matters to you.
- **Merchant webhook delivery** is in-process with retries. Deliveries in flight are lost if the process crashes. Use a transactional outbox table for guaranteed delivery.
- **Xendit refunds** are asynchronous on Xendit's side. The hub marks a refund done once Xendit accepts it.
- **Midtrans cards** need client-side tokenization (Snap/JS) and are not supported. The Midtrans Mandiri VA (`echannel`) is not mapped yet.
- **Not yet built:** listing payments, rate limiting, API key rotation, metrics.
