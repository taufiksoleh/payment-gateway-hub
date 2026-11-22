# Architecture

The hub follows Clean Architecture. Code is split into layers, and
**dependencies only point inward**: outer layers know about inner layers,
never the other way around.

```
          ┌──────────────────────────────────────────────┐
          │  app (composition root) / cmd                │  knows everything, wires it
          │ ┌──────────────────────────────────────────┐ │
          │ │ adapters: httpapi · postgres · memory ·  │ │  talk to the outside world
          │ │ midtrans · xendit · doku · notify        │ │
          │ │ ┌──────────────────────────────────────┐ │ │
          │ │ │ usecase  (+ ports.go, gateway port)  │ │ │  application logic
          │ │ │ ┌──────────────────────────────────┐ │ │ │
          │ │ │ │ domain                           │ │ │ │  business rules, no deps
          │ │ │ └──────────────────────────────────┘ │ │ │
          │ │ └──────────────────────────────────────┘ │ │
          │ └──────────────────────────────────────────┘ │
          └──────────────────────────────────────────────┘
```

| Layer | Package | May import | Must not import |
|---|---|---|---|
| Domain | `internal/domain` | standard library | anything else in the project |
| Ports | `internal/usecase/ports.go`, `internal/gateway/gateway.go` | domain | adapters |
| Use cases | `internal/usecase` | domain, ports | postgres, httpapi, vendor adapters |
| Adapters | `httpapi`, `postgres`, `memory`, `midtrans`, `xendit`, `doku`, `notify` | domain, ports | each other |
| Composition | `internal/app`, `cmd/hub` | everything | — |

A quick self-check: `go list -deps ./internal/usecase | grep payment-gateway-hub`
should only show `domain`, `gateway` (the port package), and `platform/id`.

## SOLID in this codebase

| Principle | Where you can see it |
|---|---|
| **S**ingle responsibility | Handlers only translate HTTP. Use cases hold the flow. The domain holds the rules. Each vendor has its own package, with request building, signing and webhook parsing in separate files. |
| **O**pen/closed | Adding a vendor means a new package plus one line in `app.go`. The domain, use cases and HTTP layer don't change. |
| **L**iskov substitution | Every adapter honours the same `gateway.Gateway` contract (for example, a bad signature always returns `domain.ErrInvalidSignature`). `memory` and `postgres` repositories are interchangeable. |
| **I**nterface segregation | `gateway.Refunder` is a separate, optional interface. DOKU simply does not implement it. The HTTP layer depends on small `PaymentUsecase` and `MerchantUsecase` interfaces, not concrete services. |
| **D**ependency inversion | Use cases depend on `PaymentRepository`, `GatewayResolver` and `MerchantNotifier`, all declared in `usecase/ports.go`. Concrete types are chosen only in `internal/app`. |

## Key design decisions

**Merchants bring their own vendor accounts.** The hub stores each merchant's
vendor keys (encrypted) and calls the vendor on the merchant's behalf. Money
never touches the hub, so the hub does not need a Bank Indonesia payment
licence.

**Webhook URL includes the merchant id**
(`/webhooks/{provider}/{merchant_id}`). Each vendor account belongs to one
merchant, so the hub knows which secret to verify the signature with before
it parses anything. Unknown merchants get the same 401 as a bad signature,
so outsiders cannot probe which merchant ids exist.

**The hub's payment id is the vendor-side reference.** The hub sends
`Payment.ID` (`pay_…`, 28 characters) as Midtrans `order_id`, Xendit
`reference_id` and DOKU `invoice_number`. Webhooks map straight back to the
payment, and the merchant's own `order_id` never has to fit vendor length or
format rules.

**Call the vendor first, then save.** `PaymentService.Create` saves the
payment only after the vendor accepts it. If the vendor call fails, nothing
is stored and the merchant can retry with the same `order_id`. The rare
downside is an orphan pending transaction at the vendor if the database
write fails, which simply expires.

**One state machine.** `domain.Payment.TransitionTo` is the only way to change
status. Repeating the current status is a no-op, which makes duplicate
webhooks harmless. Invalid moves (for example `paid → pending`) are rejected,
and the use case logs and ignores them.

**Optimistic locking on payments.** Webhooks, `/sync` and refunds can touch
the same payment at the same moment (vendors often retry webhooks while a
merchant is polling). Each payment has a `version`. `PaymentRepository.Update`
only writes if the stored version still matches the one that was read
(`UPDATE ... WHERE id = $1 AND version = $2`) and bumps it. Otherwise it
returns `domain.ErrConcurrentModification`.

`PaymentService.update` handles that error by reloading the payment and
re-applying the change, up to 3 attempts. Because the state machine runs
again on fresh data, a "paid" webhook racing an "expired" sync ends with
exactly one winner. The loser sees a final status and becomes a no-op, so
the merchant gets exactly one notification. If the attempts run out, the API
answers `409 concurrent_update`, and vendors simply re-send the webhook.

We use optimistic rather than pessimistic locking (`SELECT ... FOR UPDATE`)
because vendor calls happen outside any transaction, conflicts are rare, and
it keeps repositories free of transaction plumbing.

**Domain types have no JSON tags.** Wire formats live in `transport/dto`
(public API), in each vendor package (vendor API), and in `postgres`
(storage). Renaming a JSON field never touches business code.

**Errors are kinds, not HTTP codes.** The domain defines sentinel errors
(`ErrNotFound`, `ErrInvalidInput`, `ErrProvider`, and so on).
`httpapi/respond.go` is the single place that maps them to status codes.

## Request flows

### Create payment

```
POST /v1/payments
  httpapi.createPayment           decode JSON → usecase.CreatePaymentInput
  └─ PaymentService.Create
       ├─ repo.FindByOrderID      idempotency: return existing if found
       ├─ domain.NewPayment       validate
       ├─ registry.Get(provider)  pick adapter
       ├─ creds.Find              merchant's vendor keys (decrypted by repo)
       ├─ gateway.CreatePayment   vendor API call
       └─ repo.Create             save with provider ref + action
  ← 201 dto.Payment               includes action (VA number / QR / URL)
```

### Vendor webhook

```
POST /webhooks/midtrans/mch_123
  httpapi.providerWebhook         read raw body
  └─ PaymentService.HandleWebhook
       ├─ creds.Find(mch_123)     unknown → 401
       ├─ gateway.ParseWebhook    verify signature, normalize status
       ├─ repo.FindByID(event.PaymentID)
       ├─ payment.TransitionTo    no-op if unchanged
       ├─ repo.Update             version-checked; on conflict reload + retry
       └─ notifier.Notify         async, signed, with retries
  ← 200
```

## Testing strategy

| Level | What | How |
|---|---|---|
| Domain | Validation, state machine, refunds | Plain table tests |
| Use case | Idempotency, webhook flow, ownership, refunds, lock conflicts and races | `memory` repos + `gatewaytest.Fake`; a repo wrapper that injects a competing write; 40 concurrent goroutines |
| Adapter | Exact vendor wire format, signatures | `httptest.Server` pretending to be the vendor |
| HTTP | Routing, auth, error mapping, full flow | `httptest` + real use cases + memory repos |
| Storage | SQL correctness, migrations | Real PostgreSQL, gated by `TEST_DATABASE_URL` |
