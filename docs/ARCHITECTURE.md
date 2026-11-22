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
| **D**ependency inversion | Use cases depend on `PaymentRepository`, `CredentialRepository` and `GatewayResolver`, all declared in `usecase/ports.go`. Repositories depend on an `outbox.Encoder` function, not on the `notify` package. Concrete types are chosen only in `internal/app`. |

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
exactly one webhook is queued for the merchant. If the attempts run out, the API
answers `409 concurrent_update`, and vendors simply re-send the webhook.

We use optimistic rather than pessimistic locking (`SELECT ... FOR UPDATE`)
because vendor calls happen outside any transaction, conflicts are rare, and
it keeps repositories free of transaction plumbing.

**Refunds reserve before they call the vendor.** Optimistic locking alone
protects the database, not the vendor: two refunds could both pass
validation, both refund at the vendor, and only then collide. So a refund
has three steps, each saved under the lock:

```
ReserveRefund   pending_refund_amount += amount   (before the vendor call)
vendor.Refund
CompleteRefund  pending → refunded_amount          (vendor accepted)
ReleaseRefund   pending -= amount                  (vendor rejected)
```

`RefundableAmount()` subtracts reservations. A second refund that starts
while the first is at the vendor loses the lock at the reserve step, reloads,
sees nothing left to refund, and fails with 422 before it reaches the vendor.
Steps 2 and 3 use `context.WithoutCancel`, so a client disconnecting
mid-refund cannot leave a reservation behind.

**Merchant webhooks go through a transactional outbox.** Sending a webhook
straight after saving a payment has two failure modes: the process crashes
between the save and the send, so the event is lost, or the send happens but
the save rolls back, so the event is a lie. The outbox removes both:

```
Payment.TransitionTo / CompleteRefund   records a PaymentEvent (snapshot) on the payment
PaymentRepository.Create / Update       BEGIN; write payment; INSERT webhook_outbox rows; COMMIT
notify.Dispatcher (every second)        claim due rows → sign → POST → delivered / retry / dead
```

- **Events are recorded by the domain.** The use cases contain no
  notification code at all. Any status change, from any path, produces
  exactly one event.
- **One transaction.** The repository writes the payment and its events
  together. A lost optimistic lock rolls back both, so a losing update never
  queues an event.
- **Payload encoded at save time.** `notify.EncodeEvent` is injected as an
  `outbox.Encoder` into the repositories. The stored body is a snapshot of
  that moment, even if the payment changes again before delivery.
- **Several dispatchers can run at once.** Claiming is a single
  `UPDATE ... WHERE id IN (SELECT ... FOR UPDATE SKIP LOCKED)` with a 5-minute
  lease. If a dispatcher dies mid-batch, its messages become claimable again
  when the lease expires.
- **At-least-once delivery** with exponential backoff (10s doubling to a 1h
  cap, 12 attempts), then a dead-letter state. The merchant's URL and secret
  are read at delivery time, so retries follow configuration changes.
- **Housekeeping:** delivered rows are purged after 7 days. Dead rows are
  kept.

**`Idempotency-Key` is HTTP middleware, not a use case.** "Replay the response
you sent earlier" is about the API, not about payments. So it lives in
`httpapi/idempotency.go`, wraps the two routes that move money, and leaves
the use cases untouched. Its storage port is `idempotency.Store`, kept in
its own package so the `postgres` and `memory` adapters can implement it
without importing `httpapi`.

The middleware:

1. Claims the key atomically (`INSERT ... ON CONFLICT DO UPDATE WHERE`
   abandoned or expired). Exactly one concurrent request wins.
2. Runs the handler into a buffer.
3. Stores the response if it is 2xx, otherwise releases the key, then sends
   the response.

Each claim carries a random token. If a request is presumed dead after 5
minutes and its key is taken over, the original request cannot overwrite its
successor if it finishes late. Only successes are stored: a failed attempt
changed nothing a retry could duplicate, and refund reservations already
guard the one case where it might have. The same contract test
(`idempotencytest.RunStoreContract`) runs against both stores.

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
       └─ repo.Update             one transaction: payment (version-checked) + outbox event;
                                  on conflict reload + retry
  ← 200

later, notify.Dispatcher:
  outbox.ClaimDue → merchants.FindByID → Sender.Send (signed) → MarkDelivered | MarkFailed | MarkDead
```

## Testing strategy

| Level | What | How |
|---|---|---|
| Domain | Validation, state machine, refunds | Plain table tests |
| Use case | Idempotency, webhook flow, ownership, refunds, lock conflicts and races, a second refund fired while the first is at the vendor, exactly one outbox event per real change | `memory` repos + `gatewaytest.Fake`; a repo wrapper that injects a competing write; 40 concurrent goroutines |
| Adapter | Exact vendor wire format, signatures | `httptest.Server` pretending to be the vendor |
| Dispatcher | Signed delivery, backoff timing, giving up after max attempts, merchants without a URL, the run loop | memory outbox + `httptest` merchant server + fake clock |
| HTTP | Routing, auth, error mapping, full flow, Idempotency-Key replay/reuse/in-progress/retry-after-failure | `httptest` + real use cases + memory repos |
| Storage | SQL correctness, migrations, optimistic locks, outbox atomicity (a losing update writes no event), the idempotency and outbox store contracts | Real PostgreSQL, gated by `TEST_DATABASE_URL`; the same contract also runs on the memory store |
