# Xendit setup

The hub uses the Xendit **Payment Requests** API
(`POST /payment_requests`). It shows the customer the virtual account
number, QR string, payment code or e-wallet link that Xendit returns.

| | |
|---|---|
| Secrets | `secret_key`, `callback_token` |
| API | `https://api.xendit.co`, the same for test and live |
| Webhook verification | `x-callback-token` header equals your verification token |
| Refunds via the hub | E-wallets and QRIS |

Xendit has one base URL; **the secret key decides** whether a call runs in
test or live mode. The hub's `environment` setting is informational for
Xendit, but set it correctly anyway so your records are clear.

## 1. Create an account and switch to test mode

1. Sign up at [xendit.co](https://www.xendit.co). New accounts start in
   **Test Mode**, which you can use while your business verification is
   pending.
2. Make sure the dashboard toggle shows **Test Mode** for everything below.

## 2. Create a secret key

Dashboard → **Settings → Developers → API Keys** → **Generate secret key**.

- **Permissions:** give **Money-in** write access. That covers payment
  requests and refunds; nothing else is needed.
- **Copy it immediately:** confirm with your password and copy the key.
  Xendit shows it only once.
- **Prefixes:** test keys start with `xnd_development_`, live keys with
  `xnd_production_`.

([Xendit docs: API keys](https://docs.xendit.co/docs/api-keys))

## 3. Copy the webhook verification token

Dashboard → **Settings → Developers → Webhooks** (some accounts show it under
**Settings → Webhooks**) → **View Webhook Verification Token** → confirm
with your password.

Xendit sends this token in every webhook's `x-callback-token` header, and the
hub rejects webhooks that don't carry it.

([Xendit docs: webhook verification](https://docs.xendit.co/docs/verification-webhook))

## 4. Connect both to your merchant

```bash
curl -s -X PUT https://<hub>/v1/gateways/xendit \
  -H "Authorization: Bearer $API_KEY" \
  -d '{"environment": "sandbox", "secrets": {
        "secret_key": "xnd_development_...",
        "callback_token": "..."}}'
```

Note the `webhook_path` in the response, for example
`/webhooks/xendit/mch_123`.

## 5. Set the webhook URLs

On the same **Webhooks** page, find the **Payment Requests** section. Set
both the payment status and the payment method status webhooks to

```
https://<hub>/webhooks/xendit/<merchant_id>
```

then click **Test & Save**.

The hub acts on these events:

| Xendit event | Hub status |
|---|---|
| `payment.succeeded` | `paid` |
| `payment.failed` | `failed` |
| `payment_method.expired` | `expired` |

Other events are acknowledged with 200 and ignored.

**Reading the Test & Save result.** Xendit's test webhook references a
made-up payment:

| Hub answer | Meaning |
|---|---|
| `404` | The URL is reachable and the token matched; the hub just doesn't know that payment. Setup is correct. |
| `401` | The verification token saved in the hub is wrong. |
| Timeout or connection error | Xendit can't reach the hub. Check the public URL or tunnel. |

## 6. Test a payment

```bash
curl -s -X POST https://<hub>/v1/payments \
  -H "Authorization: Bearer $API_KEY" \
  -d '{"order_id": "INV-1", "provider": "xendit", "method": "virtual_account", "channel": "bni", "amount": 50000,
       "customer": {"name": "Budi"}}'
```

Then simulate the customer paying in test mode:

- **Virtual accounts and retail outlets:** use Xendit's test-mode payment
  simulation for the channel. See
  [Xendit testing: virtual accounts](https://docs.xendit.co/xenpayments/virtual-account/testing)
  and [retail outlets](https://docs.xendit.co/xenpayments/retail-outlet/testing).
- **E-wallets:** in test mode, the `redirect_url` or `deep_link_url` that
  the hub returns opens Xendit's test checkout, where you choose success or
  failure. See
  [Xendit e-wallet test guideline](https://docs.xendit.co/ewallet/integrations/test-guideline).

The payment should turn `paid`, and your merchant webhook should receive
`payment.paid`.

If the webhook doesn't arrive, `POST /v1/payments/{id}/sync` pulls the status
from Xendit. Xendit status checks use the payment request id, which the hub
stores as `provider_ref`.

## Channels

| Method | Channels | Notes |
|---|---|---|
| `virtual_account` | `bca`, `bni`, `bri`, `mandiri`, `permata`, `bsi`, `cimb` | `customer.name` is shown on the VA; it defaults to "Customer" |
| `qris` | `qris` | |
| `ewallet` | `ovo`, `dana`, `shopeepay`, `linkaja` | OVO requires `customer.phone`. The others require `return_url` |
| `retail_outlet` | `alfamart`, `indomaret` | |
| `card` | — | Not supported |

Each channel must also be activated on your Xendit account. In test mode most
are available; in live mode some need extra onboarding.

## Going live

1. Switch the dashboard to **Live Mode**. Generate a live secret key
   (`xnd_production_…`) and copy the live verification token, which is
   different from the test one.
2. Re-save the credentials:
   `PUT /v1/gateways/xendit` with `"environment": "production"`.
3. Set the live-mode Payment Requests webhooks to the same hub path.
4. Make one small real payment and refund it, to confirm the whole loop.

## Troubleshooting

- **`502 provider_error`, `INVALID_API_KEY` or 401:** wrong key, or a test
  key used where a live one is expected.
- **`502 provider_error`, `API_VALIDATION_ERROR`:** Xendit's message names
  the field. Common causes are an amount below the channel minimum or a
  channel that isn't activated.
- **`422 invalid_input` on `customer.phone` or `return_url`:** required for
  OVO, and for DANA, ShopeePay and LinkAja respectively.
- **Webhooks rejected with 401 (`invalid_signature`):** the
  `callback_token` saved in the hub isn't the current verification token.
  Test and live tokens differ.
