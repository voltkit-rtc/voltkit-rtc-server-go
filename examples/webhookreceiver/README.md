# Webhook receiver example

A `net/http` endpoint that verifies VoltKit RTC webhook deliveries with
`ParseWebhook` and handles them the way the delivery contract expects: raw
body, a list of secrets, 401 when the signature does not verify, 200 before
any work, deduplication on the event id.

## Run it

```bash
export VOLTKIT_RTC_WEBHOOK_SECRETS=whsec_...            # the signing secret from the dashboard
# during a rotation, the new secret and the previous one:
# export VOLTKIT_RTC_WEBHOOK_SECRETS=whsec_new,whsec_old
cd examples/webhookreceiver && go run .
```

It listens on `127.0.0.1:8098` (set `ADDR` to change it) at `/hooks/voltkit`.
Point your project's webhook at that URL from the dashboard and press **Send
test event**: the log prints `event webhook.test ...`.

To try it without a VoltKit project, sign a body yourself (the shared golden
vector: see `TestWebhooks_GoldenVectorByteForByte` in the SDK's tests) and
`curl` it with the `X-Stream-Signature` header; a timestamp older than five
minutes is refused with 401.

## What it shows

- **The raw body.** The signature is HMAC-SHA256 over `<timestamp>.<raw
  body>`, so the bytes must reach `ParseWebhook` exactly as received, never
  through a JSON decoder and back.
- **At most 1 MB.** A delivery is a few KB: `http.MaxBytesReader` stops
  reading at 1 MB and the receiver answers 413 (`main_test.go` checks it,
  with a signed delivery of exactly 1 MB still taken).
- **Several secrets.** When you rotate the signing secret with an overlap,
  VoltKit signs every delivery with both the new and the old secret; a
  receiver holding either verifies, so you deploy the new secret while the
  old one still works.
- **401 on `bad-signature`.** An `*voltkit.Error` means the delivery did not
  verify (not from VoltKit, or too old); a plain error means this receiver's
  setup is wrong (no secret, a negative tolerance) and is logged.
- **Answer first, then work.** A slow handler is retried, so your latency
  would become duplicate deliveries. The work goes to a channel and a worker.
- **Deduplicate on `event.ID`.** Delivery is at least once; the id is stable
  across retries. The in-memory set here stands in for your database.
- **Never assume order.** A `participant.left` can arrive before the
  `participant.joined` it followed.

Every event type and its fields: https://voltkit.in/docs/webhooks.
