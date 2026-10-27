# Token server example

A minimal `net/http` endpoint that decides the role and mints a room token
with `CreateToken`. This is the shape of the one thing every VoltKit backend
has: clients never hold the API secret, they ask your server for a token.

## Run it

```bash
export VOLTKIT_RTC_URL=https://api.voltkit.in     # your project's API base URL (Credentials tab)
export VOLTKIT_RTC_SECRET=sk_...                  # your project's API secret: this process only
cd examples/tokenserver && go run .
```

The server listens on `127.0.0.1:8099` (set `ADDR` to change it). Ask for a token:

```bash
curl -X POST -H 'X-Demo-User: user-7' http://127.0.0.1:8099/rooms/show-42/join
# {"expiresAt":...,"role":"host","token":"eyJ...","wsUrl":"wss://..."}
```

`X-Demo-User` stands in for your session: replace it, and `decideRole`, with
your own authentication and rules.

## What it shows

- `NewStreamClientFromEnv` checks the configuration at startup, so a missing
  or malformed value fails before the first user arrives.
- A `context.WithTimeout` shorter than the SDK's full retry budget, so a user
  waiting on the request is not kept for 30 seconds.
- Errors mapped by code: `IsNotFound` to 404, `IsOutOfMinutes` (the quota
  codes) to 402, `viewer-quota` to 429, `Retryable` to 503 with `Retry-After`,
  and everything else (a wrong key, a suspended account, a bad call) to a 500
  that leaks nothing. The error is logged whole: the secret is never in it.
- `Cache-Control: no-store` on the token, which is a credential for one room.
- A reply without a token or `wsUrl` is answered 502, never handed on as a
  200 the client cannot connect with. The log names the room, never the
  reply, which may hold a token. `main_test.go` checks both answers.

Nothing here is specific to `net/http`: with chi or Gin, read the path
parameter their way and keep the same error mapping.
