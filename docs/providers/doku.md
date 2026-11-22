# DOKU setup

The hub uses **DOKU Checkout**: every payment opens a DOKU-hosted payment
page, preselected to the channel you chose. The hub returns its address as
`action.redirect_url`; send the customer there.

| | |
|---|---|
| Secrets | `client_id`, `secret_key` |
| Sandbox API | `https://api-sandbox.doku.com` |
| Production API | `https://api.doku.com` |
| Webhook verification | HMAC-SHA256 `Signature` header over Client-Id, Request-Id, Request-Timestamp, the request **path** and a body digest |
| Refunds via the hub | Not supported; refund in the DOKU dashboard |

## 1. Create a sandbox account

DOKU keeps sandbox and production in **separate accounts**. Register a
**DOKU Sandbox** account for testing
([DOKU help](https://help.doku.com/en/support/solutions/articles/66000505949-is-there-a-demo-account-for-me-to-test-payment-using-doku-)).
Its back office is at `sandbox.doku.com`.

## 2. Copy the Client ID and Secret Key

Sandbox back office → **Settings → API Keys** (also at
`sandbox.doku.com/bo/developer/api-keys`) → copy the **Client ID** (starts
with `BRN-`) and the **Secret Key**.

Regenerating the Secret Key invalidates the old one immediately. If you
rotate it, update the hub straight away.

([DOKU docs: set up integration](https://docs.doku.com/get-started/manage-business/set-up-integration))

## 3. Connect them to your merchant

```bash
curl -s -X PUT https://<hub>/v1/gateways/doku \
  -H "Authorization: Bearer $API_KEY" \
  -d '{"environment": "sandbox", "secrets": {
        "client_id": "BRN-...",
        "secret_key": "SK-..."}}'
```

Note the `webhook_path` in the response, for example
`/webhooks/doku/mch_123`.

## 4. Set the notification URL

In the back office's **payment settings**, set the **Notification URL**
(DOKU applies it to all payment methods) to

```
https://<hub>/webhooks/doku/<merchant_id>
```

DOKU requires an address reachable from the internet, and recommends
`https`.

([DOKU docs: notification URL](https://dashboard.doku.com/docs/docs/http-notification/setup-notification-url))

> **The path must arrive unchanged.** DOKU signs the notification over the
> request path (`Request-Target`). If a reverse proxy rewrites or strips a
> path prefix before the hub, every notification fails with
> `invalid_signature`. Forward `/webhooks/doku/...` as is.

## 5. Enable payment methods

Activate the channels you'll use (VA banks, QRIS, e-wallets, retail
outlets) in the back office's payment settings. DOKU Checkout only offers
methods that are active on the account.

## 6. Test a payment

```bash
curl -s -X POST https://<hub>/v1/payments \
  -H "Authorization: Bearer $API_KEY" \
  -d '{"order_id": "INV-1", "provider": "doku", "method": "virtual_account", "channel": "bri", "amount": 50000,
       "return_url": "https://your-shop.example/thanks"}'
```

1. Open `action.redirect_url`; it shows the DOKU payment page with the VA
   number.
2. Pay it with the DOKU simulator: back office → **Settings → Payment
   Settings → Simulator**, choose the method, then **Simulate**
   ([DOKU docs: simulate transactions](https://docs.doku.com/get-started/manage-business/set-up-integration/simulate-transactions)).
3. The payment should turn `paid`, and your merchant webhook should receive
   `payment.paid`. `return_url` is where DOKU sends the customer afterwards.

If the notification doesn't arrive, `POST /v1/payments/{id}/sync` asks DOKU
for the status of the order.

## Channels

| Method | Channels |
|---|---|
| `virtual_account` | `bca`, `mandiri`, `bri`, `bni`, `permata`, `cimb` |
| `qris` | `qris` |
| `ewallet` | `ovo`, `dana`, `shopeepay`, `linkaja` |
| `retail_outlet` | `alfamart`, `indomaret` |
| `card` | `card` (on the hosted page) |

## Statuses

| DOKU `transaction.status` | Hub status |
|---|---|
| `SUCCESS` | `paid` |
| `FAILED` | `failed` |
| `EXPIRED` | `expired` |
| anything else (`PENDING`, …) | `pending` |

## Going live

1. In your **production** DOKU account, copy the production Client ID and
   Secret Key.
2. Re-save the credentials:
   `PUT /v1/gateways/doku` with `"environment": "production"`.
3. Set the production Notification URL to the same hub path, and activate
   the payment methods in production.
4. Make one small real payment, then refund it in the DOKU dashboard.

## Troubleshooting

- **`502 provider_error`, HTTP 401 or "invalid signature":** the Client ID
  and Secret Key don't belong together, or sandbox keys were saved as
  `production` (the reverse fails too).
- **Notifications rejected with 401 (`invalid_signature`):** check, in
  order:
  1. The `client_id` in the hub matches the account sending the
     notification.
  2. The `secret_key` hasn't been regenerated since you saved it.
  3. No proxy changes the request path (see step 4).
- **The hosted page shows no payment method:** that method isn't active on
  the DOKU account.
- **Refund returns `422 unsupported`:** expected. Refund in the DOKU
  dashboard.
