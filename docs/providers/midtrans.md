# Midtrans setup

The hub uses the Midtrans **Core API**: it creates the charge itself and
shows the customer the virtual account number, QR string or deep link.
There's no Snap popup.

| | |
|---|---|
| Secrets | `server_key` |
| Sandbox API | `https://api.sandbox.midtrans.com` |
| Production API | `https://api.midtrans.com` |
| Webhook verification | `signature_key` = SHA-512(`order_id` + `status_code` + `gross_amount` + server key) |
| Refunds via the hub | Cards, GoPay, ShopeePay, QRIS |

## 1. Create a sandbox account

1. Sign up at [midtrans.com](https://midtrans.com) and open the
   **sandbox** dashboard (`dashboard.sandbox.midtrans.com`).
2. Sandbox and production are separate environments with **different keys**.
   Everything below is per environment: do it once for sandbox and again
   when you go live.

## 2. Copy the server key

Dashboard → **Settings → Access Keys** → copy the **Server Key**. Sandbox
server keys start with `SB-Mid-server-`.

The hub doesn't use the Client Key, because cards (which need client-side
tokenization) aren't supported through the hub.

([Midtrans docs: access keys](https://docs.midtrans.com/docs/switching-to-production-mode))

## 3. Connect it to your merchant

```bash
curl -s -X PUT https://<hub>/v1/gateways/midtrans \
  -H "Authorization: Bearer $API_KEY" \
  -d '{"environment": "sandbox", "secrets": {"server_key": "SB-Mid-server-..."}}'
```

Note the `webhook_path` in the response, for example
`/webhooks/midtrans/mch_123`.

## 4. Set the notification URL

Dashboard → **Settings → Payment → Notification URL** → enter

```
https://<hub>/webhooks/midtrans/<merchant_id>
```

and save.

- **One hub per URL:** Midtrans accepts several URLs separated by commas. Point only one of them at the hub.
- **Leave unchanged:** the "Finish", "Unfinish" and "Error" redirect URLs are for Snap and don't matter to the hub.

([Midtrans docs: notification URL](https://docs.midtrans.com/reference/setting-up-notification-url))

## 5. Test a payment

Create a payment through the hub:

```bash
curl -s -X POST https://<hub>/v1/payments \
  -H "Authorization: Bearer $API_KEY" \
  -d '{"order_id": "INV-1", "provider": "midtrans", "method": "virtual_account", "channel": "bca", "amount": 50000}'
```

Then pay it in the
[Midtrans sandbox simulator](https://docs.midtrans.com/docs/testing-payment-on-sandbox):

- **Virtual account:** open the VA simulator for the bank, enter
  `action.virtual_account_number`, click **Inquire**, then pay.
- **QRIS:** open the QRIS simulator
  (`simulator.sandbox.midtrans.com/openapi/qris/index`), paste
  `action.qr_image_url`, then click **Scan QR**.

Within a few seconds the payment should turn `paid`
(`GET /v1/payments/{id}`), and your merchant webhook should receive
`payment.paid`.

**Testing without a Midtrans account:** `make up && make demo` runs the same
flow against the bundled mock (`dev/mockmidtrans`).

## Channels

| Method | Channels | Notes |
|---|---|---|
| `virtual_account` | `bca`, `bni`, `bri`, `cimb`, `permata` | Mandiri (`echannel`) isn't mapped yet |
| `qris` | `qris` | Acquirer: GoPay |
| `ewallet` | `gopay`, `shopeepay` | Pass `return_url` to send the customer back after paying |
| `retail_outlet` | `alfamart`, `indomaret` | `description` appears as the store message |
| `card` | — | Needs Snap or JS tokenization; not supported |

## Statuses

| Midtrans `transaction_status` | Hub status |
|---|---|
| `pending`, `authorize` | `pending` |
| `capture` (fraud `accept`), `settlement` | `paid` |
| `capture` (fraud `challenge`) | `pending` |
| `deny`, `failure` | `failed` |
| `cancel` | `cancelled` |
| `expire` | `expired` |
| `refund` / `partial_refund` | `refunded` / `partially_refunded` |

## Going live

1. In the **production** dashboard, copy the production Server Key (it has
   no `SB-` prefix).
2. Re-save the credentials:
   `PUT /v1/gateways/midtrans` with `"environment": "production"`.
3. Set the production dashboard's notification URL to the same hub path.
4. Make one small real payment and refund it, to confirm the whole loop.

## Troubleshooting

- **`502 provider_error`, HTTP 401:** the server key doesn't match the
  environment. A sandbox key saved as `production` (or the reverse) is the
  usual cause.
- **Notifications rejected with 401 (`invalid_signature`):** the server key
  saved in the hub differs from the one in the dashboard sending the
  notification.
- **Notifications rejected with 404:** dashboard "test notification" buttons
  send made-up order ids. Real payments created through the hub are matched.
