# Setting up payment providers

Every merchant connects **their own** account at each provider. The hub
stores the keys encrypted and calls the provider on the merchant's behalf.
Money settles directly from the provider to the merchant's account; it never
passes through the hub.

Setup guides:

- [Midtrans](midtrans.md)
- [Xendit](xendit.md)
- [DOKU](doku.md)

## At a glance

| | Midtrans | Xendit | DOKU |
|---|---|---|---|
| Secrets the hub needs | `server_key` | `secret_key`, `callback_token` | `client_id`, `secret_key` |
| Sandbox vs production | Separate keys and URLs | One URL; the key decides test or live | Separate keys and URLs |
| How the customer pays | Account number, QR string, deep link or payment code from the API | Same as Midtrans | Always a hosted DOKU page (`redirect_url`) |
| Webhook verification | SHA-512 `signature_key` in the body | `x-callback-token` header | HMAC-SHA256 `Signature` header covering the path |
| Refunds through the hub | Cards, GoPay, ShopeePay, QRIS | E-wallets, QRIS | Not supported; use the DOKU dashboard |

Supported channels per provider are listed in the main [README](../../README.md#supported-channels).

## The steps are the same for every provider

1. **Get sandbox keys** at the provider (see its guide).
2. **Connect them to your merchant** in the hub:

   ```bash
   curl -s -X PUT https://<hub>/v1/gateways/<provider> \
     -H "Authorization: Bearer $API_KEY" \
     -d '{"environment": "sandbox", "secrets": { ... }}'
   ```

   The hub checks that every required secret is present. It does not call
   the provider at this point, so a wrong key only shows up on the first
   payment.

   The response includes `webhook_path`, for example
   `/webhooks/midtrans/mch_123`.
3. **Set the provider's notification URL** to `https://<hub><webhook_path>`.
   Without it, payments stay `pending` until you call
   `POST /v1/payments/{id}/sync`.
4. **Create a test payment** through the hub and pay it with the provider's
   sandbox simulator. The payment should turn `paid`, and your merchant
   webhook should fire.
5. **Go live:** repeat steps 2 and 3 with `"environment": "production"`,
   production keys and the production notification URL.

## Testing a real sandbox from your laptop

Providers must reach the hub over the internet to deliver notifications.
For local testing, expose the hub with a tunnel such as
[cloudflared](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/do-more-with-tunnels/trycloudflare/),
[ngrok](https://ngrok.com) or [localhost.run](https://localhost.run), and use
the tunnel URL as `<hub>` in step 3:

```bash
make up                                   # local stack on :8080
cloudflared tunnel --url http://localhost:8080
```

The Docker stack sends Midtrans calls to the bundled mock by default. To use
the real Midtrans sandbox instead:

```bash
MIDTRANS_SANDBOX_URL=https://api.sandbox.midtrans.com make up
```

Xendit and DOKU already talk to their real sandboxes in the Docker stack.

## When something goes wrong

The hub's error codes point at the cause:

| What you see | Likely cause | Fix |
|---|---|---|
| `422 invalid_input`, field `secrets.<name>` when saving credentials | A required secret is missing | Send every secret listed for the provider |
| `422 invalid_input`, field `provider`: "no … credentials configured" | The merchant hasn't connected this provider | `PUT /v1/gateways/<provider>` first |
| `422 unsupported`: "… does not support method/channel" | The provider doesn't offer that channel through the hub | Choose another provider or channel (see the channel table) |
| `502 provider_error` with HTTP 401 inside | Wrong key, or sandbox keys saved as `production` (or the reverse) | Re-save the credentials with the right keys and environment |
| `502 provider_error` with another message | The provider rejected the request | Read the provider's message; it's passed through as is |
| The provider dashboard shows webhook deliveries failing with **401** | The webhook secret doesn't match, or the URL points at another merchant | Check the secret and the merchant id in the notification URL |
| Webhook deliveries failing with **404** | The notification is for a payment the hub didn't create, such as a dashboard test | Expected for those; real payments created through the hub are matched |
| Payment stays `pending` after paying | The notification URL isn't set or isn't reachable | Fix the URL; meanwhile `POST /v1/payments/{id}/sync` pulls the status |

> **Status:** the adapters follow each provider's documentation and are
> tested against recorded responses. They have **not yet been run against
> live sandbox accounts**. If a real sandbox rejects a field, please open an
> issue with the provider's error message.
