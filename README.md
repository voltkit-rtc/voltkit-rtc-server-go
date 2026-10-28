# VoltKit RTC server SDK for Go

Server SDK for [VoltKit RTC](https://voltkit.in/rtc): rooms, tokens, chat,
usage, billing and verified webhooks, from a Go backend. Go 1.22 or newer,
standard library only.

Your backend does two things: decide who may do what, and hand the client a
short-lived token. It is never in the media path: there is no stream to proxy
and nothing that scales with viewers.

```bash
go get github.com/voltkit-rtc/voltkit-rtc-server-go
```

```go
import voltkit "github.com/voltkit-rtc/voltkit-rtc-server-go"
```

| | |
| --- | --- |
| [Quickstart](#quickstart) | `NewStreamClient` `NewStreamClientFromEnv` |
| [Rooms](#rooms) | `EnsureRoom` `CreateRoom` `CreateToken` `GetRoom` `ListRooms` `ListParticipants` `DeleteRoom` |
| [Chat](#chat) | `CreateChatToken` `SendMessage` `ListMessages` `DeleteMessage` `MuteChat` `UnmuteChat` `ListChatMutes` `GetChatInfo` |
| [Usage](#usage) | `GetUsage` `GetUsageSeries` |
| [Billing](#billing) | `GetBalance` `GetRates` `ParseCu` |
| [Configuration](#configuration) | `Config` `Ptr` |
| [Errors](#errors) | `Error` `ErrorCodes` `RESTErrorCodes` `ErrorInfo` |
| [Webhooks](#webhooks) | `ParseWebhook` `VerifyWebhook` `VerifyOptions` `StreamEvent` |
| [Broadcast](#broadcast) | `CreateStream` `CreateStreamToken` `GetStream` `ListStreams` `UpdateStream` `DeleteStream` (not offered on VoltKit cloud today) |

## Quickstart

Create one client when your server starts and share it: it is safe for
concurrent use. Both values come from your project's **Credentials** tab; the
secret is server-side only.

```go
client, err := voltkit.NewStreamClient(voltkit.Config{
	BaseURL: os.Getenv("VOLTKIT_RTC_URL"),    // e.g. https://api.voltkit.in
	APIKey:  os.Getenv("VOLTKIT_RTC_SECRET"), // sk_...: never in a web page or a mobile app
})
if err != nil {
	log.Fatal(err) // a missing or malformed option, caught at startup; the error never quotes the key
}
```

`NewStreamClientFromEnv()` reads the same two variables, plus the optional
`VOLTKIT_API_URL` for [GetRates](#rates), and nothing else.

A "go live" and a "let someone in", with the role your rules decide:

```go
room, err := client.EnsureRoom(ctx, voltkit.EnsureRoomParams{
	RoomID:       "show-" + show.ID, // YOUR id, so webhooks read back usefully
	HostIdentity: "user-" + user.ID,
})
// room.HostToken, room.WSURL, room.ExpiresAt, room.Existed

tok, err := client.CreateToken(ctx, voltkit.CreateTokenParams{
	RoomID:   "show-" + show.ID,
	Identity: "user-" + viewer.ID,
	Role:     voltkit.RoleViewer, // host, cohost, moderator or viewer; signed into the token
})
// tok.Token, tok.WSURL, tok.ExpiresAt: hand them to that one client, never log them
```

A complete token endpoint on `net/http`, with the error mapping, is in
[`examples/tokenserver`](examples/tokenserver); a webhook receiver in
[`examples/webhookreceiver`](examples/webhookreceiver).

Every method takes a `context.Context` first. Cancelling it, or its deadline
passing, stops the call and its retries and returns the context's error.

## Rooms

A room is the interactive kind of session: everyone in it can be given the
right to speak, and media goes through the SFU.

```go
// Create the room, or get it back with a fresh host token if it already exists.
room, err := client.EnsureRoom(ctx, voltkit.EnsureRoomParams{RoomID: "show-42", HostIdentity: "user-7"})
if room.Existed { /* a host reconnecting, not a new "go live" */ }

// The strict version: fails with room-exists (409) if the id is taken; makes up an id if RoomID is empty.
room, err = client.CreateRoom(ctx, voltkit.CreateRoomParams{RoomID: "show-42", HostIdentity: "user-7"})

// Opaque, room-scoped state every participant receives on join. Applied only when the room is created.
room, err = client.EnsureRoom(ctx, voltkit.EnsureRoomParams{
	RoomID: "show-42", HostIdentity: "user-7",
	Metadata: map[string]any{"seatFormat": 20},
})

// Let someone in. Your backend decides the role; the SFU enforces it.
tok, err := client.CreateToken(ctx, voltkit.CreateTokenParams{RoomID: "show-42", Identity: "user-99", Role: voltkit.RoleViewer})

// The authoritative live view: the room, who is in it, what they publish.
view, err := client.GetRoom(ctx, "show-42")
// view.Room.Participants, view.Participants, view.Producers

list, err := client.ListRooms(ctx)   // list.Rooms: every live room (the wire's object, kept)
people, err := client.ListParticipants(ctx, "show-42") // []Participant: identity, role, handRaised, muted, joinedAt, metadata
err = client.DeleteRoom(ctx, "show-42")                // ends it for everyone
```

- A room token lasts ten minutes and authorises the handshake; a connected
  client is refreshed by the controller, so a call can run for hours.
- Poll `GetRoom` for a live count rather than adding up webhook joins and
  leaves: delivery is at-least-once and unordered, so a counter built from
  events drifts.
- `Metadata` is any value `encoding/json` can encode (a map, a struct, a
  `json.RawMessage`); replies carry it back as `json.RawMessage`. Left nil, it
  is left out of the body; a nil `json.RawMessage` or a typed nil pointer was
  given, and is sent as `null`.
- Room ids are `[A-Za-z0-9._-]{1,128}` and scoped to your project: another
  project's room is a 404, never a 403.

## Chat

Every room has a chat channel of the same name, and a channel does not need a
room: `show-42` can be chat for a broadcast, a lobby, or nothing but chat.
Someone in the room already has chat on their room connection; for anyone
else, mint a chat token:

```go
chat, err := client.CreateChatToken(ctx, voltkit.CreateChatTokenParams{Channel: "show-42", Identity: "user-99"})
// chat.Token, chat.WSURL (ends in /ws/chat), chat.Role (viewer by default), chat.ExpiresAt
```

Your backend takes part without holding a socket:

```go
_, err = client.SendMessage(ctx, voltkit.SendMessageParams{Channel: "show-42", Text: "The show starts in 5 minutes", Type: "system"})
_, err = client.SendMessage(ctx, voltkit.SendMessageParams{Channel: "show-42", Type: "gift", From: "user-7", Metadata: map[string]any{"gift": "rose", "count": 5}})
_, err = client.SendMessage(ctx, voltkit.SendMessageParams{Channel: "show-42", Type: "reaction", Metadata: map[string]any{"emoji": "heart"}, Ephemeral: true}) // not stored
_, err = client.SendMessage(ctx, voltkit.SendMessageParams{Channel: "show-42", To: "user-13", Text: "Please keep it friendly"})                                  // a direct message

messages, err := client.ListMessages(ctx, "show-42", &voltkit.ListMessagesOptions{Limit: 100}) // oldest first
older, err := client.ListMessages(ctx, "show-42", &voltkit.ListMessagesOptions{Before: messages[0].ID})
err = client.DeleteMessage(ctx, "show-42", messageID)
err = client.MuteChat(ctx, "show-42", "user-13")   // survives reconnecting; they can still read
err = client.UnmuteChat(ctx, "show-42", "user-13")
muted, err := client.ListChatMutes(ctx, "show-42") // []string
info, err := client.GetChatInfo(ctx, "show-42")    // info.Members
```

`From` defaults to `system`, and you may speak as one of your own users: your
backend is the authority on who they are, which is what makes a gift
announcement trustworthy in a way a client-sent one is not. Backend messages
skip the per-connection rate limit and the mute list. `ListMessages` returns
the newest `Limit` messages (50 by default, at most 100), oldest first, direct
messages included: your backend owns the conversation. A message needs text or
metadata; text is capped at 4096 bytes. `SendMessage` is
[never retried](#calls-that-are-never-repeated).

## Usage

```go
usage, err := client.GetUsage(ctx)
// usage.MinutesRemaining (-1 on an unlimited plan), usage.Today, usage.Month, usage.Lifetime (participantSeconds, chatMessages, ...)
// usage.Allowance: free minutes left today and this month, the paid balance, the caps, and Blocked with the reason

days, err := client.GetUsageSeries(ctx, 30) // one entry per UTC day, oldest first, ending today; 0 means 30, at most 90
```

Days with no usage are present and zero, so a chart drawn from the series has
no gaps. When an account has nothing left, new rooms, tokens and messages fail
with [`IsOutOfMinutes`](#errors), and anything live is warned and then ended.

## Billing

### Balance

```go
b, err := client.GetBalance(ctx)
// b.BillingMode ("legacy" or "wallet"), b.CanJoin, b.Blocked (*string, nil when CanJoin),
// b.Legacy (minutes spent first), b.Wallet (*WalletFunds, nil for a legacy project),
// b.Burn, b.RemainingSeconds, b.WalletLow, b.PricingVersion (*int), b.UpdatedAt
```

What your project can spend right now, to show a balance, warn before a call
runs out, or decide whether to start one. Read only: recharges happen on the
website. `CanJoin` says whether a newcomer would be admitted; when it is false,
`Blocked` is the code a join or a token would be refused with:

```go
if !b.CanJoin {
	switch *b.Blocked {
	case "tenant-suspended":    // 403: contact support
	case "billing-unavailable": // 503: billing is being set up
	default:                    // quota-exhausted, daily-limit-reached, monthly-limit-reached: recharge
	}
}
```

**Amounts are strings.** Money is in credit units (cu), 1 credit = 6,000,000
cu (`voltkit.CuPerCredit`), and every cu amount (`PaidCu`, `AvailableCu`,
`ProjectCuPerSecond`, ...) is a decimal string, because a JSON number is a
float and rounds a large balance without telling you. Do arithmetic with
`ParseCu` and `math/big`; show `AvailableCredits`, which is already formatted:

```go
if b.Wallet != nil {
	cu, err := voltkit.ParseCu(b.Wallet.AvailableCu)              // *big.Int
	credits := new(big.Int).Div(cu, big.NewInt(voltkit.CuPerCredit)) // whole credits
	fmt.Println(b.Wallet.AvailableCredits)                        // "31", "10.62": at most four decimals, rounded down
}
```

**How a reply is decoded.** Into the typed structs, by the standard
library's `encoding/json`, whose rules differ from the other VoltKit SDKs' in
ways only a reply that is not the API's can show. A JSON key matches a field
**case-insensitively**, Unicode case folding included: `"Price"` and `"PRICE"`
fill `Price`, and `"muſic"` (with a long s) fills `Music`; when a reply holds
two spellings of one field, the last one wins. A key that appears twice keeps
its last value, and a duplicated object is merged field by field rather than
replaced. A field the struct does not have is dropped: an add-on id newer than
this release is not in `Addons` (upgrade to read it). A number where the
struct has a string (a price, a cu amount) is refused with `http-<status>`
rather than converted, so a string field is always the server's own text. The
API sends exact lower-camel-case keys once each, so none of this changes what
a real reply decodes to.

### Rates

```go
client, err := voltkit.NewStreamClient(voltkit.Config{
	BaseURL:      os.Getenv("VOLTKIT_RTC_URL"),
	APIKey:       os.Getenv("VOLTKIT_RTC_SECRET"),
	RatesBaseURL: "https://api.voltkit.in", // VoltKit's website API: the origin, without /api/v1
})
rates, err := client.GetRates(ctx)
// rates.Version, rates.Tiers ([{ID: "video_30", Credits: "99.00", CuPerParticipantSecond: "9900", ...}]),
// rates.Surcharges, rates.Addons (nil before the Music add-on; .Music: Price "10.00", CuPerParticipantSecond "1000",
// Unit, AppliesTo, IncludedIn), rates.IdleTier, rates.Modes, rates.Conversion, rates.Notes
```

**The Music add-on (0.1.2).** `rates.Addons.Music` prices in-app music
(`publishMusic` in the client SDKs): Music is an add-on on standard and HD voice
(₹10 per 1,000 participant-minutes while music plays, enabled in the project's
Settings → Media and quality); included with video. `Price` is in credits per
1,000 participant-minutes and `CuPerParticipantSecond` the same as an exact
integer; `AppliesTo` names the voice tiers it is billed on top of and
`IncludedIn` the video tiers that include it. `Addons` is nil for a price list
from before the add-on, so check it before reading `Music`. `music` is an
add-on id, not a tier: a usage line with `tier: "music"` (the settlement feed,
the console's usage, the invoice) is the add-on's participant-minutes next to
the participant's own tier line.

The public price list: each tier in credits per 1,000 participant-minutes,
the media modes that are switched on, and the conversion for new recharges.
Prices are set on the website, so `GetRates` reads
`GET /api/v1/billing/rtc/rates/current` from the website API named by
`RatesBaseURL`, and sends it **no credential**: no API key, no `Authorization`
header. Without `RatesBaseURL` it fails at once with `rates-unconfigured` and
sends nothing. The website caches the list for 30 seconds; cache it on your
side too if you read it often. A tier's `CuPerParticipantSecond` is exact, so
a cost is integer arithmetic:

```go
price, _ := voltkit.ParseCu(video30.CuPerParticipantSecond)
cost := new(big.Int).Mul(price, big.NewInt(4*30*60)) // 4 people for 30 minutes, in cu
```

## Configuration

```go
client, err := voltkit.NewStreamClient(voltkit.Config{
	BaseURL:      "https://api.voltkit.in",
	APIKey:       secret,
	RatesBaseURL: "https://api.voltkit.in", // optional, for GetRates
	Timeout:      5 * time.Second,          // each attempt: headers and body (default 10 s)
	Retries:      voltkit.Ptr(1),           // at most one more attempt (default 2; Ptr(0) turns retries off)
	HTTPClient:   &http.Client{Transport: myTransport},
})
```

| Field | Default | What it does |
| --- | --- | --- |
| `BaseURL` | required | The controller's origin, exact scheme and host, as the Credentials tab shows it. The API key is sent here and nowhere else. A trailing `/` is dropped. |
| `APIKey` | required | Your project's API secret. Whitespace around it is ignored. |
| `RatesBaseURL` | unset | The website API's origin, where `GetRates` reads the price list with no credential. `""` and `"/"` mean unset. |
| `Timeout` | 10 s | The deadline for each attempt, from sending the request to reading the last byte of the reply. Zero means the default; above 2^31−1 ms it is held there. |
| `Retries` | `nil` = 2 | How many more attempts a call that is safe to repeat gets after a transient failure. `voltkit.Ptr(0)` turns retries off. |
| `HTTPClient` | a fresh `http.Client` | Your own client, for a proxy, custom TLS or instrumentation. It is copied, never modified: the copy for the controller never follows a redirect, the copy for the rates host keeps your redirect policy, and both leave the deadline to the context. |

The constructor checks every field and returns a plain `error` for a mistake,
so a wrong value fails at startup instead of on a user's first call; none of
these errors quotes the key. A `Config` prints under every `fmt` verb,
marshals to JSON and logs through `log/slog` with the key hidden; it is still
a plain struct, so keep it out of reflection-based dumps (see
[Safe to log](#safe-to-log)). A client is safe to share between goroutines and
holds no state outside a call; it logs nothing. Every request carries
`User-Agent: voltkit-rtc-go/<Version>` and nothing else about your system.

## Errors

A call that fails returns an `*voltkit.Error`. Branch on `Code` or the
predicates, and log the message: it is always safe to log.

```go
tok, err := client.CreateToken(ctx, voltkit.CreateTokenParams{RoomID: roomID, Identity: identity, Role: role})
if err != nil {
	var e *voltkit.Error
	if !errors.As(err, &e) {
		return err // not an answer from VoltKit: the context was cancelled, or a bug in this handler
	}
	log.Printf("join: %v", e) // the API secret is never in it
	switch {
	case e.IsNotFound():          // 404 no-room: this show has ended
	case e.IsOutOfMinutes():      // 402 quota-exhausted, daily-limit-reached, monthly-limit-reached
	case e.Code == "viewer-quota": // 429: this show is full
	case e.IsCapacityExhausted(): // 503 capacity-exhausted, no-nodes: alert, new rooms are failing right now
	case e.Retryable:             // the SDK has already retried: try again later, with Retry-After
	default:                      // 401, 403, 400: a problem on your side
	}
}
```

| Field or method | Meaning |
| --- | --- |
| `Code` | The server's code (`no-room`, `quota-exhausted`, ...). `network` when no reply arrived; `http-<status>` when a reply named no code; `redirected`; `rates-unconfigured`; `bad-request` with `Status` 0 for an id refused before sending. A code this release does not know is passed through unchanged. |
| `Status` | The HTTP status. `0` when nothing reached the server or the SDK refused the call itself; the 3xx for `redirected`. |
| `Message` | `<METHOD> <path>: <what went wrong>`, such as `POST /v1/rooms/show-42/tokens: room not found`. Also what `Error()` returns. |
| `Retryable` | `true` for `network`, `429` and `5xx` (except `billing-unavailable`): trying again later could work. On a call the SDK repeats, it means the retries have been spent. |
| `IsNotFound()` | `Status` is 404: no such room, stream or message, or not yours. `broadcast-disabled` and `chat-disabled` are 404s too, so test `IsBroadcastDisabled()` first. |
| `IsOutOfMinutes()` | `Status` is 402, or the code is `quota-exhausted`, `daily-limit-reached` or `monthly-limit-reached`. |
| `IsQuota()` | The code is `room-quota`, or `Status` is 429: `rate-limited`, `viewer-quota` and `stream-quota` included. |
| `IsCapacityExhausted()` | `capacity-exhausted` or `no-nodes`: no capacity for a new room right now. The one to alert on. |
| `IsBroadcastDisabled()` | `broadcast-disabled`: broadcast is not switched on for this deployment. |
| `IsBillingUnavailable()` | `billing-unavailable`: the project is billed from the wallet and no price list is active yet, or the controller has no wallet billing. |

The predicates are safe on a nil `*Error`.

### What to do, code by code

Every code a call can fail with, from the shared registry (`RESTErrorCodes`).
"Retried" means the SDK retries it on a call that is safe to repeat;
`CreateRoom`, `CreateStream` and `SendMessage` are
[sent once](#calls-that-are-never-repeated).

| Code | Status | Retried | What it means, and what to do |
| --- | --- | --- | --- |
| `unauthorized` | 401 | no | A wrong or revoked secret, or `BaseURL` is another project's. Check `VOLTKIT_RTC_SECRET`; alert yourself, tell the user only that something failed. A cheap read at startup, such as `GetUsage`, catches it before your users do. |
| `tenant-suspended` | 403 | no | The account is suspended. Contact support. |
| `quota-exhausted` | 402 | no | Out of minutes or credits. The project owner recharges; `GetBalance` says so before anyone tries. |
| `daily-limit-reached` | 402 | no | The daily cap is reached. Wait for the UTC day to reset, or raise the cap. |
| `monthly-limit-reached` | 402 | no | The monthly cap is reached. Wait for the month to reset, or raise the cap. |
| `billing-unavailable` | 503 | **no** | Wallet billing is not ready (no active price list, or no wallet billing on this controller). An operator fixes it; a retry does not help. |
| `no-room` | 404 | no | The room is gone, or was never yours. Treat the show as ended. |
| `no-message` | 404 | no | The message is gone. Nothing to do. |
| `not-found` | 404 | no | The mute, or whatever the call named, is not there. Nothing to do. |
| `no-stream` | 404 | no | The stream is gone. Treat it as off air. |
| `broadcast-disabled` | 404 | no | Broadcast is not available on this deployment (VoltKit cloud today). Do not retry. |
| `chat-disabled` | 404 | no | Chat is not available on this deployment. Do not retry. |
| `room-exists` | 409 | no | The id is taken. Use `EnsureRoom`, or another id. |
| `stream-exists` | 409 | no | The stream id is taken. Use the existing stream, or another id. |
| `bad-request` | 400 | no | Fix the call: the message names the field (an id outside `[A-Za-z0-9._-]{1,128}`, an empty message, a bad cursor). With `Status` 0, the SDK refused an id of `""`, `"."` or `".."` before sending. |
| `bad-role` | 400 | no | `Role` is not `host`, `cohost`, `moderator` or `viewer`. Fix the call. |
| `operator-only` | 403 | no | An operator endpoint with a project key. Not something an SDK call reaches. |
| `bad-signature` | 401 | no | From `ParseWebhook`: the delivery did not verify. Answer 401; VoltKit retries it. |
| `rate-limited` | 429 | yes | Your project's API rate limit (600 per minute by default). Slow down; spread calls out. |
| `room-quota` | 429 | yes | At your concurrent-room limit. End rooms you no longer need, or raise the limit. |
| `viewer-quota` | 429 | yes | The room is at its viewer limit. Tell the viewer it is full. |
| `stream-quota` | 429 | yes | At your concurrent-stream limit. Delete streams you no longer need. |
| `capacity-exhausted` | 503 | yes | The fleet is full: new rooms are failing right now. Ask the user to try again shortly, and alert. |
| `no-nodes` | 503 | yes | No media node is registered. As above. |
| `unavailable` | 503 | yes | The service could not answer (a read failed). Try again later; tell us if it repeats. |
| `internal` | 500 | yes | A fault on the service. Try again later; tell us if it repeats. |
| `network` | 0 | yes | No reply: DNS, a refused or dropped connection, the timeout. Try again later; if it persists, check your server's outbound network. |
| `redirected` | 3xx | no | The server answered a request carrying the key with a redirect. Fix `BaseURL`: see [Redirects are never followed](#redirects-are-never-followed). |
| `rates-unconfigured` | 0 | no | `GetRates` without `RatesBaseURL`. Set it to the website API's origin. |
| `http-<status>` | that status | only a 429 or 5xx | A reply that named no code: a proxy's error page, a success that is not JSON, or a reply over 8 MiB, the most the SDK reads of any reply ("the reply is too large"). Check `BaseURL`, and anything between your server and VoltKit. |
| `RATE_LIMITED`, `NOT_FOUND`, ... | as sent | by status | The website API's own codes, from `GetRates`, passed through unchanged. |

`ErrorCodes` is the whole shared registry in its order, `RESTErrorCodes` the
codes above, and `ErrorInfo(code)` gives a code's origin and flags, for a
bounded metrics label or a dashboard:

```go
label := "other"
if _, known := voltkit.ErrorInfo(e.Code); known {
	label = e.Code
}
```

### Mistakes caught before a request

These are plain `error` values, never an `*Error`, so a handler cannot
mistake them for an answer from VoltKit. They are never retried and never
quote the key.

| Mistake | How it fails |
| --- | --- |
| `BaseURL` or `APIKey` empty; `BaseURL` or `RatesBaseURL` not an absolute http(s) URL with a host, or carrying a query or fragment (a path prefix is fine) | `NewStreamClient` returns an error |
| `APIKey` only whitespace, such as a bare line break from a file | `NewStreamClient` returns an error |
| `APIKey` holding a character no HTTP header can carry (a line break or NUL inside, another control character, DEL, anything above U+00FF), a backslash, or a character outside ASCII such as an accented letter (no VoltKit key holds one) | `NewStreamClient` returns an error saying to check how the key is loaded |
| `Timeout` negative, `Retries` negative | `NewStreamClient` returns an error |
| An id of `""`, `"."` or `".."` for a room, channel, message, identity or stream | The call returns `*Error` `bad-request`, `Status` 0, and sends nothing: a URL reads those as steps through the path. Every other id is percent-encoded and sent as given |
| A body `encoding/json` cannot encode (a channel, a func, a cycle in `Metadata`) | The call returns a plain error naming it, such as `PUT /v1/rooms/show-42: the body cannot be sent as JSON: ...`, with the encoder's error wrapped |
| `GetRates` without `RatesBaseURL` | The call returns `*Error` `rates-unconfigured`, `Status` 0 |
| `VerifyWebhook` with no secret, or a negative `Tolerance` | Panics (a loud mistake, like the Node SDK's `TypeError`); `ParseWebhook` returns a plain error instead |

## Retries

Transient failures are retried with jittered backoff on every call that is
safe to repeat: token minting, reads (`GetBalance` and `GetRates` included),
`EnsureRoom`, `UpdateStream`, mutes and deletes. Transient means a `network`
error (no reply, a dropped connection, the timeout), a `429`, or a `5xx` other
than `billing-unavailable`, the one 503 that lasts until an operator acts.
Any other `4xx` is a decision the server made and will make again;
`redirected`, a `2xx` that is not JSON and a reply over 8 MiB (the most the
SDK reads of any reply) are not transient either.

The waits are 200 ms, 400 ms, 800 ms, ... plus up to 100 ms at random, so a
fleet of servers does not retry in step (the doubling is held at 2^31−1 ms,
the deadline's cap, so a very large `Retries` never turns into instant
retries). A call can take up to
`(Retries + 1) × Timeout` plus the waits: about 31 seconds with the defaults.
In a request a user is waiting on, lower both, or give the call a context with
a deadline: the SDK stops retrying the moment the context ends and returns the
context's error. When an `*Error` reaches you with `Retryable` true, the
retries have been spent: try again later, from a job or the user's next
action, not at once in a loop.

### Calls that are never repeated

`CreateRoom`, `CreateStream` and `SendMessage` are sent once, however they
fail. If a reply is lost, repeating them would turn a success into a spurious
`room-exists` or `stream-exists`, or post the message twice. Every other call
is safe to repeat: tokens are independent, reads change nothing, `EnsureRoom`
returns the room that exists, and a mute or a delete done twice ends the same
way.

When one of the three fails with `Retryable` true, you decide:

- A `429`, whatever its code, is a refusal: nothing was done. Send it again after a pause.
- A `network` error or a `5xx` may have done the work before the reply was lost. Check first:

| Call | Check, then repeat |
| --- | --- |
| `CreateRoom` | Call `EnsureRoom` with the same `RoomID`: it returns the room if your call made it, or makes it. Use ids nobody else uses. |
| `CreateStream` | Call `GetStream`. If the stream is there, mint a publish URL with `CreateStreamToken{Action: voltkit.ActionPublish}`, since only `CreateStream` returns one. If `IsNotFound`, create it again. |
| `SendMessage` | Put your own id in `Metadata`, and look for it in `ListMessages` before sending again. An `Ephemeral` message is never stored, so it cannot be found: send it again or drop it. |

```go
// Announce a gift once, even when a reply is lost.
func announceGift(ctx context.Context, channel string, gift Gift) (*voltkit.ChatMessage, error) {
	msg := voltkit.SendMessageParams{Channel: channel, Type: "gift", From: gift.From, Metadata: map[string]any{"giftId": gift.ID, "gift": gift.Name}}
	sent, err := client.SendMessage(ctx, msg)
	var e *voltkit.Error
	if err == nil || !errors.As(err, &e) || !e.Retryable {
		return sent, err
	}
	if e.Status != 429 {
		// It may have been stored before the reply was lost: look before sending again.
		recent, err := client.ListMessages(ctx, channel, &voltkit.ListMessagesOptions{Limit: 100})
		if err != nil {
			return nil, err
		}
		for i := range recent {
			var meta struct{ GiftID string `json:"giftId"` }
			if json.Unmarshal(recent[i].Metadata, &meta) == nil && meta.GiftID == gift.ID {
				return &recent[i], nil
			}
		}
	}
	time.Sleep(time.Second)
	return client.SendMessage(ctx, msg)
}
```

### Deletes that lost their reply

`DeleteRoom`, `DeleteMessage`, `UnmuteChat` and `DeleteStream` hold across a
lost reply. When an attempt fails in a way that may have reached the server (a
`network` error or a `5xx`) and the retry answers 404, the first attempt did
the deleting, so the call succeeds. A 404 on the first attempt still fails, as
does one after a 429 (which deleted nothing) or one saying chat or broadcast
is switched off (`chat-disabled`, `broadcast-disabled`). To treat "already
gone" as done, catch it:

```go
if err := client.DeleteRoom(ctx, roomID); err != nil {
	var e *voltkit.Error
	if !errors.As(err, &e) || !e.IsNotFound() {
		return err
	}
	// it had already ended
}
```

## Where your API secret goes

The SDK sends your API secret in the `X-API-Key` header of requests to
`BaseURL`, and nowhere else:

- `GetRates` sends nothing to `RatesBaseURL`.
- A redirect is never followed with it (below).
- It never appears in an error or in the printed client (below).

### Redirects are never followed

A request that carries the secret never follows a redirect: Go's client would
keep `X-API-Key` on the way to whatever host the `Location` names. A 3xx
answer fails at once with the code `redirected`: `Status` is the 3xx, the
message names where it pointed, and it is not retried, because the same
`BaseURL` redirects the same way. If your `BaseURL` reached the service
through a redirect (an `http://` address upgraded to `https://`, or a `www.`
rewrite in front), set it to the exact scheme and host the Credentials tab
shows. `GetRates` sends no secret and may follow redirects.

### Safe to log

An error never carries your API secret, or the credentials of a URL
(`user:pass@` in `BaseURL` or `RatesBaseURL`), so `log.Printf("%v", err)` and
your logger can take it whole. Wherever the transport, a dropped connection, a
redirect or the service's reply quotes either one, `Message` and `Code` say
`[redacted]` instead, however it was encoded: as is, URL-encoded by any
encoder in either hex case, form-encoded, JSON-escaped (`\/`, `+`), as
HTML character references (`&sol;`, `&#47;`, `&#x2F;`), or any of those
escaped once more (`%252F`, `\&quot;`). A key over 256 characters is not
looked for: words at least as long as it are shown as
`[not shown: it could not be checked for the API key]`, and shorter words,
such as error codes, are kept; a key holding a tab could be split across
words, so then the whole message is withheld unless it is shorter than the
key. A long message is cut only after that, so a cut cannot leave part of the
secret behind.

The client object keeps its secrets out of sight too. `String`, `GoString`,
`Format`, `MarshalJSON` and `LogValue` all give the same redacted view, so
`fmt.Printf` with any verb, `json.Marshal(client)` and
`slog.Info("ready", "client", client)` print
`voltkit.StreamClient{BaseURL: https://[redacted]@x.test, RatesBaseURL: unset, Timeout: 10s, Retries: 2}`
and never the key. The key, the URLs as given and the redactor live in
closures, which reflection cannot read: a debugger's variable view or a
reflection-based dumper sees four function addresses. A `Config` prints,
marshals and logs the same way, but it is a plain struct whose `APIKey` field
reflection can read, so keep a `Config` out of dumps. Requests still go to the
URLs as given.

What a call returns is another matter: `Token`, `HostToken` and
`Publish.WHIPURL` are credentials for one room or stream. Hand each to the one
client that needs it, and never log them.

## Webhooks

VoltKit signs every delivery: `X-Stream-Signature: t=<unix seconds>,v1=<hex>`,
where `v1` is HMAC-SHA256 over `<timestamp>.<raw body>` with your signing
secret. `ParseWebhook` verifies and decodes in one step, so a handler cannot
forget to check:

```go
// The current secret, and the previous one during a rotation (see below).
secrets := []string{os.Getenv("VOLTKIT_RTC_WEBHOOK_SECRET"), os.Getenv("VOLTKIT_RTC_WEBHOOK_SECRET_OLD")}

mux.HandleFunc("POST /hooks/voltkit", func(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20)) // the raw bytes: the signature covers them exactly
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	event, err := voltkit.ParseWebhook(body, r.Header.Get("X-Stream-Signature"), secrets, nil)
	if err != nil {
		var e *voltkit.Error
		if errors.As(err, &e) {
			w.WriteHeader(http.StatusUnauthorized) // bad-signature: not from VoltKit, or too old
			return
		}
		log.Printf("webhook: %v", err) // a plain error: no secret, a negative tolerance; fix your setup
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK) // answer first
	queue <- event               // then do the work
})
```

- **Pass the raw body.** A parsed and re-serialized body can never verify.
  Read `r.Body` yourself, before any JSON middleware; with a framework, use
  its raw-body accessor (chi and Gin hand you the same `r.Body`).
- **One header value.** VoltKit sends the signature as one header, which
  `r.Header.Get` returns. If something in front of your server splits it into
  two header lines, pass `strings.Join(r.Header.Values("X-Stream-Signature"), ",")`
  instead: that is what the Node SDK does with a list.
- **What it checks.** The timestamp must be within `Tolerance` of your clock
  (`DefaultTolerance`, five minutes, in either direction, inclusive), so a
  captured request cannot be replayed later, and a `v1` signature must match
  one of your secrets, compared in constant time with `hmac.Equal`, every
  secret against every signature. Empty entries in `secrets` are skipped, so
  an unset `VOLTKIT_RTC_WEBHOOK_SECRET_OLD` is fine.
- **What it returns.** The event, or an `*Error` of code `bad-signature`
  (`Status` 401) when the delivery does not verify: answer 401, and VoltKit
  retries it. A plain error (no secret at all, a negative `Tolerance`, a
  verified body that is not JSON) is a mistake in your setup.
- **`VerifyWebhook`** takes the same arguments and returns `true` or `false`
  instead of the event; a mistake in the setup makes it panic rather than
  return a `false` that would look like an attack.
- **Options.** `&voltkit.VerifyOptions{Tolerance: voltkit.Ptr(10 * time.Minute)}`
  widens the window; `Now` replaces the clock in tests.

The event is a `StreamEvent`: `ID`, `Type`, `TenantID`, `RoomID`, `CreatedAt`
(unix milliseconds) and `Data` as `json.RawMessage`, whose shape depends on
`Type`. Three things to get right on your side:

- **Answer first.** A slow handler is retried, so your latency becomes
  duplicate deliveries. Retries come at 5 s, 30 s, 2 min, 10 min and 30 min;
  a 410 disables the endpoint.
- **Deduplicate on `event.ID`.** Delivery is at-least-once by design; the id
  is stable across retries (also in the `X-Stream-Delivery` header, with
  `X-Stream-Attempt`).
- **Do not assume order.** A `participant.left` can arrive before the
  `participant.joined` it followed; pair them by identity and `joinedAt`.

Every event type and its fields: https://voltkit.in/docs/webhooks. A complete
receiver is in [`examples/webhookreceiver`](examples/webhookreceiver).

### Rotating the signing secret

When you press **Rotate secret** with an overlap (24 hours by default), VoltKit
signs every delivery with both the new secret and the previous one until the
overlap ends: `t=<ts>,v1=<new>,v1=<previous>`. `ParseWebhook` and
`VerifyWebhook` accept a delivery when any `v1` value matches any secret you
pass, so you can rotate without dropping a delivery:

1. Deploy the handler above, which takes a list. With the old-secret variable
   unset, it verifies exactly as before.
2. Press **Rotate secret**. Move the current secret to
   `VOLTKIT_RTC_WEBHOOK_SECRET_OLD`, put the new one in
   `VOLTKIT_RTC_WEBHOOK_SECRET`, and deploy before the overlap ends.
3. After the overlap, unset `VOLTKIT_RTC_WEBHOOK_SECRET_OLD`.

## Testing

Point the client at an `httptest.Server` that answers the way the service
does: `{ "error": "<code>", "message": "..." }` with the status, for each
branch of your error handling. `Retries: voltkit.Ptr(0)` keeps a failing test
fast.

```go
srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	calls = append(calls, r.Method+" "+r.URL.RequestURI())
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":"no-room","message":"room not found"}`))
}))
defer srv.Close()

client, _ := voltkit.NewStreamClient(voltkit.Config{BaseURL: srv.URL, APIKey: "sk_test", Retries: voltkit.Ptr(0)})
_, err := client.CreateToken(ctx, voltkit.CreateTokenParams{RoomID: "show-42", Identity: "user-99", Role: voltkit.RoleViewer})
var e *voltkit.Error
ended := errors.As(err, &e) && e.IsNotFound() // true; calls: ["POST /v1/rooms/show-42/tokens"]
```

For a test with no listener at all, pass `HTTPClient: &http.Client{Transport: rt}`
where `rt` is your `http.RoundTripper`. To test a webhook handler, sign a
body yourself: `hex(HMAC-SHA256(secret, "<t>." + body))` in the header
`t=<t>,v1=<hex>`, and pass `&voltkit.VerifyOptions{Now: ...}` to pin the clock.

## Versions

`voltkit.Version` is the module's version, and `User-Agent:
voltkit-rtc-go/<Version>` goes on every request. This is 0.1.2, at parity
with `@voltkit-rtc/server` 0.4.9; [CHANGELOG.md](CHANGELOG.md) lists what each
release changes. Until 1.0, a breaking change is a minor bump and everything
else a patch.

## Help

Documentation: https://voltkit.in/docs/go-sdk. Questions and bug reports:
contact@voltkit.in. Send the error's `Code` and `Message`, which never contain
your secret, and never the secret itself.

## Broadcast

**Not offered on VoltKit cloud today.** These methods are kept for parity with
the API and the other server SDKs; on a deployment without a media server
every one of them fails with `broadcast-disabled` (`IsBroadcastDisabled`).

A broadcast is one publisher and an audience of any size, served as LL-HLS
through a CDN and over WebRTC.

```go
live, err := client.CreateStream(ctx, voltkit.CreateStreamParams{StreamID: "show-42", Playback: voltkit.PlaybackPublic})
// live.StreamKey, live.State ("idle"), live.PlaybackURLs.LLHLS, live.PlaybackURLs.WebRTC,
// live.Publish.WHIPURL (carries the publish token: returned ONLY here; treat it like a password)

pub, err := client.CreateStreamToken(ctx, voltkit.CreateStreamTokenParams{StreamID: "show-42", Action: voltkit.ActionPublish}) // a fresh publish URL
play, err := client.CreateStreamToken(ctx, voltkit.CreateStreamTokenParams{StreamID: "show-42", Identity: "user-99", TTLSeconds: 3600}) // play (the default), for Playback token
// play.LLHLS, play.WebRTC, play.Token, play.ExpiresAt

s, err := client.GetStream(ctx, "show-42")                                                      // state, viewers, peakViewers
streams, err := client.ListStreams(ctx)                                                         // []BroadcastStream
s, err = client.UpdateStream(ctx, "show-42", voltkit.UpdateStreamParams{Playback: voltkit.PlaybackToken}) // who may watch, or metadata
err = client.DeleteStream(ctx, "show-42")                                                       // takes it off air; its URLs stop working
```

`TTLSeconds` is capped at 24 hours. `Viewers` counts the playback sessions the
media server serves itself, not an audience behind a CDN. `CreateStream` is
[never retried](#calls-that-are-never-repeated), for the same reason as
`CreateRoom`.
