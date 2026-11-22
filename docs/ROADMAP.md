# Roadmap

What to build next, ordered by the merchant problems each step solves. The hub
already covers the basics: one API over Midtrans, Xendit and DOKU, signed
webhooks that are never lost, safe retries, refunds, rate limits, metrics and
request logs. The steps below turn it from "one API for three gateways" into
something that keeps sales flowing, explains where the money went, and costs
less to run.

Last reviewed: November 2026.

## Principles

These hold for every step and decide what we do *not* build.

- **The hub never holds money.** Funds always flow directly between the
  merchant's own gateway account and the customer. Holding or forwarding funds
  would make the hub a payment service provider, which needs a Bank Indonesia
  licence. Payouts, splits and escrow are therefore out of scope; they belong
  to the gateways.
- **No raw card data.** Cards stay tokenized at the gateway, which keeps the
  hub out of PCI DSS scope.
- **Merchants keep their own gateway contracts.** The hub adds value through
  routing, visibility and a single integration, not by reselling rates.
- **Every new capability is a port plus adapters**, as described in
  [ARCHITECTURE.md](ARCHITECTURE.md), so each piece can be written and tested
  by hand.

## Overview

| Phase | Merchant problem | Main deliverables | Size |
|---|---|---|---|
| 0 ✅ | "Can I trust this in production?" | Sandbox verification, failure reasons, refund reconciliation, Redis limiter | M |
| 1 | "A gateway outage costs me sales" | Multi-provider failover, health tracking, routing rules | L |
| 2 | "I can't tell where my money is" | Settlement import, automatic reconciliation, fee tracking, exports | L |
| 3 | "My customers want to pay differently" | Payment links, hosted checkout, recurring payments | L |
| 4 | "Fees eat my margin" | Fee tables, cost-aware routing, QRIS zero-MDR routing | M |
| 5 | "Integration takes too long" | OpenAPI spec, SDKs, e-commerce plugins, merchant dashboard | L |
| 6 | "Am I compliant?" | PDP data retention and erasure, audit log, SNAP readiness, KMS | M |
| Console | "I'm not a developer; I just want to see my payments" | [Web console](#web-console) for merchants and operators, in three milestones alongside the phases | L |
| — | "My gateway isn't supported" | More adapters, as demand appears | S each |

Phases 1 and 2 are the core value of a hub: a single gateway can't fail over
to its competitor, and it can't reconcile across providers. Phase 0 comes first
because those features are useless if the base isn't verified.

## Phase 0: production readiness ✅

Built in November 2026. One item still needs people with vendor accounts.

| Item | Status |
|---|---|
| Sandbox checks for all three adapters | `make sandbox-test` built; **not yet run against live sandbox accounts** |
| `failure_reason` from provider responses | Done: Midtrans and Xendit codes mapped onto shared reasons |
| Background refund reconciliation | Done: a refund whose answer was lost stays reserved and is resent with the same refund id until the vendor accepts it |
| Require `Idempotency-Key` per merchant | Done: `PATCH /v1/settings` |
| Claim `order_id` before calling the provider | Done: a short-lived claim table; the second request gets `409` |
| Shared rate limiter (Redis) | Done: `REDIS_URL` |
| Midtrans Mandiri bill payment (`echannel`) | Done |
| CI builds and scans the Docker image | Done: Trivy on every push, image pushed to ghcr.io on `v*` tags |

## Phase 1: never lose a sale to an outage

**Problem.** Gateways do go down. Xendit's status page, for example, recorded
a QR payments disruption in Indonesia in May 2026. With one gateway, every
payment during that window fails. A merchant connected through the hub
usually holds accounts with several gateways but still has to switch between
them by hand.

**Build.**

1. **Provider health.** Track success rate and latency per provider and
   channel from data the hub already records (`hub_provider_requests_total`).
   Add a circuit breaker per channel: after N consecutive provider errors,
   mark it unhealthy for a cool-down period.
2. **Failover on create.** When `CreatePayment` fails with a provider error
   (never on a validation error) and the merchant has another healthy provider
   for the same method, retry there. Record each attempt on the payment so
   the merchant can see which provider served it.
3. **Routing rules per merchant.**
   - **Rule format:** evaluated in order, e.g. "QRIS → Xendit, else Midtrans";
     "amount > 10,000,000 → DOKU"; "e-wallet `gopay` → Midtrans only".
   - **Creating payments:** `provider` becomes optional on `POST /v1/payments`;
     when it's omitted, the rules decide.
4. **Success-rate routing (later).** Weight providers by recent success
   rate per channel. Ship this only after the simple rules have produced
   enough data to compare against.

**Architecture.**
- **Router:** a `usecase.Router` port picks an ordered list of providers. A
  rules adapter comes first, a scoring adapter later.
- **Payments:** `PaymentService.Create` walks the list.
- **Health:** health state lives behind a `HealthTracker` port, in memory
  first and in Redis for multiple instances.

**Risks.**
- **Double charges on failover:** failover must never happen once a provider
  might have created the charge, for example on a timeout after the request
  was sent. Treat a timeout as unknown: run a status check first, and fail
  over only on a clear rejection.
- **Different payment instructions:** VA numbers differ per provider, so
  failover only makes sense before the customer sees payment instructions.

## Phase 2: know where the money is

**Problem.** Each gateway settles on its own schedule, in its own report
format, with its own fees. Merchants reconcile by hand in spreadsheets: did
every paid order actually reach the bank account, and what did it cost? A
neutral layer above the gateways can do this; no single gateway can, because
each sees only its own transactions.

**Build.**

1. **Settlement import.** A `SettlementSource` port per provider pulls
   settlement and payout reports, through the provider's reporting API where
   one exists, or by uploading the provider's CSV export. Store each line with
   gross amount, fee, tax, net amount and settlement date.
2. **Automatic matching.** Match settlement lines to payments by provider
   reference and amount. Flag four kinds of mismatch:
   - paid but not settled after the expected delay;
   - settled but unknown to the hub;
   - amount mismatch;
   - fee different from the merchant's contract.
3. **Reconciliation API and webhook.** `GET /v1/reconciliation?date=` returns
   matched, pending and mismatched totals, and a daily
   `reconciliation.completed` event lists the mismatches.
4. **Exports.** CSV export of payments, refunds and settlements for
   accounting tools, with consistent columns across providers.

**Architecture.**
- **Storage:** new `settlement` and `settlement_line` tables. Settlement data
  is immutable once imported; corrections arrive as new lines.
- **Jobs:** import and matching run as background jobs, using the same lease
  pattern as the webhook dispatcher.

## Phase 3: more ways to get paid

| Feature | Problem it solves | Approach |
|---|---|---|
| **Payment links** | Small merchants sell over WhatsApp and Instagram and have no checkout page | `POST /v1/payment-links` returns a short URL. Opening it shows the methods the merchant enabled across all connected providers, then creates the payment through the normal flow. Optional amount, expiry and single or multiple use |
| **Hosted checkout** | Every merchant rebuilds a "choose a payment method" page | A small server-rendered page served by the hub, themable per merchant. The same page backs payment links |
| **Recurring payments** | Subscriptions need saved payment methods, and each gateway tokenizes differently | Unified `payment_method` tokens wrapping gateway tokens: GoPay recurring through Midtrans or Xendit, cards through Xendit or DOKU. Merchant-initiated charges with `POST /v1/payment-methods/{id}/charges`. Plans and schedules come later. Note that GoPay requires completion in the GoPay app from January 2026 |
| **Status pages for customers** | Customers ask the merchant "did my payment go through?" | Public, unguessable payment status URL with the payment instructions (VA number, QR code, expiry) |

The hub stores only the gateway's token and a display hint (`gopay ••••1234`),
never the underlying credential.

## Phase 4: lower costs

**Problem.** Fees differ by provider, method and contract, and they change. Bank
Indonesia extended zero-percent QRIS merchant discount rates from 1 October
2026; reports describe the thresholds as up to Rp500,000 for micro merchants
and up to Rp100,000 for all merchants. Merchants who don't steer suitable
payments to the cheapest option leave money on the table.

**Build.**
- **Fee tables per merchant and provider:** percentage, flat amount and
  caps per method and channel, entered from the merchant's own contract.
- **Fee estimate:** included on each payment, then confirmed against the
  settlement data from Phase 2.
- **Cost-aware routing:** a routing strategy that prefers the cheapest healthy
  provider, plugged into the Phase 1 router.
- **Method hints:** an optional "suggest QRIS" flag on hosted checkout for
  amounts under the zero-MDR thresholds.
- **Fee report:** a monthly report of what each provider and method cost,
  and what routing saved.

## Phase 5: faster integration

| Item | Why |
|---|---|
| **OpenAPI 3 spec** and generated docs ✅ | Done: [docs/api/openapi.yaml](api/openapi.yaml), served at `/docs`, kept in step with the code by tests. Postman and Insomnia import the document directly |
| **SDKs:** PHP first, then Node.js, Go and Python | PHP (Laravel) is common among Indonesian SMB stacks. Each SDK includes webhook signature verification |
| **Plugins:** WooCommerce, then Laravel | Lets non-developers adopt the hub |
| **Web console** | See [Web console](#web-console) below |
| **Test mode** | The `dev/` mock providers become a first-class sandbox, so merchants can simulate paid, expired and failed payments without vendor sandbox accounts |

## Phase 6: compliance and trust

| Item | Why |
|---|---|
| **Personal data retention and erasure** | Indonesia's Personal Data Protection law (UU PDP) has been fully in force since October 2024, and Government Regulation 33/2026 adds sanctions from 16 January 2027. Add configurable retention for customer name, email and phone; erasure on request; and a data export per customer |
| **Encrypt customer personal data at rest** | Credentials are already encrypted; customer contact data isn't yet |
| **Audit log** | Who changed credentials, rotated keys, replayed webhooks or edited routing rules, and when. Required by most enterprise merchants |
| **Key management** | Move `ENCRYPTION_KEY` to a KMS (AWS KMS, GCP KMS or Vault) with key rotation |
| **SNAP readiness** | Bank Indonesia's open API payment standard (SNAP) governs VA, direct debit and e-wallet APIs. Gateways such as DOKU publish SNAP migration guides. Adapters should target each gateway's SNAP endpoints as they become the default, without changing the hub's own API |
| **Admin roles** | Replace the single admin token with named admin users and scoped permissions |

## Web console

**Problem.** Today everything is done with `curl`. The people who run a shop
day to day (owners, finance, customer support) need to look up a payment,
issue a refund or check why a webhook failed without writing code. Operators
running the hub need to onboard merchants and see its health at a glance.

**Two consoles, one app.**
- **Merchant console:** each merchant sees and manages only its own account.
- **Operator console:** for whoever runs the hub. It handles merchant
  onboarding, system health and support. It replaces the single admin token
  with named operator accounts.

### Milestones

The console grows with the API; each milestone uses only features that
already exist when it starts.

| Milestone | When | Pages |
|---|---|---|
| **C1: see and act** | Now; the API is ready | Overview, payments (search, detail, refund, sync), webhooks (deliveries, payload, replay), gateways (connect wizard), API keys, settings, team |
| **C2: routing and money** | With Phases 1–2 | Provider health, routing rules editor, reconciliation (matched, pending, mismatched), settlement uploads, CSV exports |
| **C3: grow and comply** | With Phases 3, 4 and 6 | Payment links, checkout branding, fee tables and fee report, audit log, customer data export and erasure |

### C1 pages

| Page | What it shows and does |
|---|---|
| **Overview** | Today and this month: volume, count, success rate per provider and method, failure reasons, pending refunds, dead webhooks. Each tile links to the filtered list |
| **Payments** | Search by order id, filter like `GET /v1/payments`. Detail view: status timeline, customer, payment instructions, vendor reference, webhooks sent. **Refund** and **Sync** buttons (owner and finance roles only) |
| **Webhooks** | Deliveries with status, attempts and last error. Exact payload. **Replay** one, or every dead one |
| **Gateways** | One connect wizard per vendor, linked to the [setup guides](providers/README.md): paste keys, copy the notification URL, **Test connection**. Secrets are never shown again |
| **API keys** | Rotate with a chosen grace period, revoke, see last use. A new key is shown once, with a copy button |
| **Settings** | Webhook URL, webhook secret rotation, `require_idempotency_key` |
| **Team** | Invite by email, assign roles, remove members |

### Design

- **Plain Go, no build step.**
  - **Rendering:** pages are server-rendered with `html/template`, with
    [htmx](https://htmx.org) for partial updates.
  - **Packaging:** embedded in the hub binary with `go:embed`, served at
    `/console`. No Node toolchain, which matches how the rest of the project is
    built and keeps it hand-maintainable.
- **Same use cases as the API.** Console handlers call the existing
  `usecase` services through the same ports, so a refund from the console
  follows the same reservation, idempotency and reconciliation rules as one
  from the API. Every action the console offers is also available in the
  public API.
- **People log in; programs use API keys.**
  - **Login:** console users with email and password, hashed with argon2id,
    plus TOTP two-factor (required for owners and operators).
  - **Sessions:** server-side sessions in Postgres behind an `HttpOnly`,
    `Secure`, `SameSite=Strict` cookie, with CSRF tokens on every form.
  - **Brute-force limits:** login attempts are rate limited per account and
    per IP.
- **Roles.**

  | Role | Can |
  |---|---|
  | Owner | Everything, including team, gateways and API keys |
  | Developer | Gateways, API keys, webhooks, settings |
  | Finance | Payments, refunds, reconciliation, exports |
  | Viewer | Read-only; customer contact details masked |

- **Personal data.** Lists mask customer email and phone (`bu***@gmail.com`);
  full details appear only on a payment's page, and every view of them is
  written to the audit log. This follows the PDP work in Phase 6.
- **Deployment.**
  - **Switch:** `CONSOLE_ENABLED`, on by default.
  - **Separate address:** the console can be served on its own listener
    (`CONSOLE_ADDR`), so it can sit behind a VPN or SSO proxy while the API
    stays public.
  - **Security headers:** a strict Content-Security-Policy, with no
    third-party scripts at runtime.

### New API and storage it needs

| Need | For |
|---|---|
| `GET /v1/stats?from=&to=&group_by=provider,method,status` | Overview tiles, and useful to API users too |
| `GET /v1/payments/{id}/events` | The status timeline (from the events already written to the outbox) |
| `PATCH /v1/settings` with `webhook_url`; `POST /v1/settings/webhook-secret/rotate` | Settings page |
| `POST /v1/gateways/{provider}/test` | **Test connection**: a harmless authenticated call to the vendor |
| `console_users`, `memberships`, `sessions`, `invites` tables | Login, teams, roles |
| Audit log (Phase 6, moved earlier) | Who refunded, rotated a key or changed credentials |

### Order of work

1. **Accounts and access:** console users, sessions, roles and audit log,
   since every page depends on them.
2. **C1 pages:** payments and webhooks first (what support asks about most),
   then gateways, keys, settings and team.
3. **Overview page,** once `/v1/stats` exists.
4. **Operator console:** merchant list and onboarding, provider health, outbox
   backlog, then retire `ADMIN_TOKEN` for named operators.

**Risks.**
- **Account takeover** now exposes refunds to a password. Mitigations: 2FA,
  role checks on every action, refund confirmation showing amount and order,
  and alerts on new logins and key rotations.
- **Scope creep** into a general admin panel. Each page must map to a merchant
  problem in this roadmap.

## More gateways

Add adapters when merchants ask for them, following
[ADDING_A_GATEWAY.md](ADDING_A_GATEWAY.md). Candidates often used by Indonesian
small businesses include iPaymu, Duitku, Tripay and Faspay. Check current
market share and API quality before committing; this list isn't researched
in depth.

## Not planned

- Holding, splitting or paying out funds (needs a payment service licence).
- Storing raw card numbers (PCI DSS scope).
- Fraud scoring. The gateways already do this, and the hub sees too little
  data to do it better. Revisit once routing data exists.

## Sources

- [Bank Indonesia: zero-percent QRIS MDR expansion, August 2026](https://www.bi.go.id/en/publikasi/ruang-media/news-release/Pages/sp_2815926.aspx)
- [BCA: QRIS MDR adjustment notice, September 2026](https://www.bca.co.id/en/informasi/news-and-features/2026/09/01/18/50/informasi-penyesuaian-merchant-discount-rate-mdr-transaksi-qris)
- [Kontan: QRIS 0% MDR thresholds](https://keuangan.kontan.co.id/news/kebijakan-mdr-qris-0-diperluas-bank-hadapi-tekanan-fee-dari-bisnis-acquiring)
- [Xendit status: QR payments disruption, Indonesia](https://status.xendit.co/incidents/2y7stm938cr9)
- [e27: reconciliation across payment rails](https://e27.co/same-failure-better-clothes-matchmade-io-on-selling-indonesian-finance-tech-in-singapore-20260925/)
- [Bank Indonesia: SNAP](https://www.bi.go.id/en/layanan/Standar/SNAP/default.aspx)
- [DOKU: SNAP migration guide](https://docs.doku.com/miscellaneous/snap-migration)
- [Xendit docs: GoPay and GoPay recurring](https://docs.xendit.co/docs/gopay)
- [Juspay: payment orchestration in 2026](https://juspay.io/blog/why-global-merchants-can-t-afford-to-ignore-payment-orchestration-in-2026)
- [Hyperswitch (open-source orchestration, for comparison)](https://github.com/juspay/hyperswitch)
- [Rajah & Tann: Indonesia PDP law updates](https://www.rajahtannasia.com/viewpoints/indonesias-pdp-law-updates-dpa-u-s-trade‑related-data-transfers-and-recent-court-rulings/)
- [htmx](https://htmx.org)
- [RSM: PP 33/2026 implementation priorities](https://www.rsm.global/indonesia/sites/default/files/media/publications/Client%20Alert/2026/RSM%20Indonesia%20Client%20Alert%20%20-%20PP%20332026%20Six%20Implementation%20Priorities%20before%2016%20January%202027.pdf)
