# Changelog

## 0.1.3 (2026-10-31)

From the testers' report of 2026-10-31 (G012-1 to G012-5, X-1, X-2) and the lead's decisions D4 to D9 of that
round, at parity with `@voltkit-rtc/server` 0.4.10. Each fix has a test that failed on 0.1.2, in
`round_2026_10_31_test.go`, and the shared vectors' new groups have loops in `vectors_test.go`.

- **A 2xx reply that is not what the call needs is refused** (G012-1, X-1, D4). `CreateToken` answered `{}` returned
  a `RoomToken` with an empty `Token` and a nil error; `EnsureRoom` then added `Existed`. Now each call checks the
  reply before decoding it: a non-empty string `token` (the three token calls), `roomId` (`CreateRoom`,
  `EnsureRoom`), `id` (`SendMessage`), `streamId` (`CreateStream`, `GetStream`, `UpdateStream`); an object `room`
  (`GetRoom`); an array `rooms` (`ListRooms`); a string `channel` (`GetChatInfo`); an object (`GetUsage`,
  `GetBalance`); and for the four lists and `GetUsageSeries`, an array or nothing (missing is still an empty slice;
  `null` is refused). Anything else, an empty body and `null` included, fails with `http-<status>`, not retried,
  `<METHOD> <path>: the reply is not the expected shape: "token" is not a non-empty string` (or `it is not an
  object`), quoting nothing of the reply. Deletes and mutes take anything, as before; `GetRates` keeps its own
  check and now says "carries no rates" for any JSON that is not its envelope (a list was "could not be decoded").
  Where a reply spells the needed field twice in different case, the exact key's value is the one returned
  (`encoding/json` matches case-insensitively and keeps the last). Vectors `replies` (356 cases), `TestVectors_Replies`.
- **The key, HTML-escaped and then by Go's JSON encoder, was not redacted** (G012-2, Medium). The escaper for Go's
  `json.Marshal` had `&` where it needed `\u0026`, so `sk\u0026lowbar;<rest of key>` reached `Error()`,
  every `fmt` verb and JSON. One entry in `redact.go`, written out with its backslash. Numeric references with any
  number of leading zeros (`&#x005F;`, `&#00000095;`, and those escaped again) are redacted too (N049-5).
- **A webhook tolerance of 0 refused nearly every delivery** (G012-3, X-2). Freshness now compares whole seconds:
  `|floor(now) - t| <= Tolerance`. A `Tolerance` of 0 accepts the whole current second (0 of 200 before), and
  300.999 s old passes with the default, as `VerifyOptions` always promised. Vectors `webhooks.wholeSeconds`.
- **A whitespace-only webhook secret is no secret** (D5). `" "` worked as an HMAC key; now it never verifies anything,
  beside a real secret or alone, and with no other secret it is the missing-secret error (`VerifyWebhook` panics,
  `ParseWebhook` returns a plain error). A secret with whitespace around real text stays the whole string.
- **Metadata that would encode as `null` is left out** (G012-4). A nil map, slice or pointer in `Metadata`, a nil
  `json.RawMessage` or `json.RawMessage("null")` went out as `"metadata":null`, which the server stored as the
  metadata, and which let a `SendMessage` with neither text nor metadata past the server's empty-message check.
  All five calls that take `Metadata`; the caller's params are not changed.
- **URL passwords holding `@`, `/`, `?` or `#` are redacted whole** (D7, N049-3). `https://user:hunter2/x@h` showed
  the password. The rule is the Node SDK's pattern, which RE2 cannot write (no lookahead), as linear code; 150,000
  random strings gave Node's exact output, and a fuzz test compares it with a direct transcription of the pattern.
  `https://h/a@b`, `https://h:8080/a@b` and `https://user:123/x@h` (a port and a path) are shown as they are.
- **A bad `BaseURL` or `RatesBaseURL` fails in `NewStreamClient`, by the spec's rules** (D8). Whitespace at both
  ends is dropped (a line read from a file), then one trailing slash. Refused, in words that never quote the URL: a
  space, control character, backslash or character outside ASCII; no `http://` or `https://`; a query or fragment;
  no host, or a port that is not 1 to 65535 (`https://x.test:` when the port's variable was unset); anything
  `net/url` refuses. Only whitespace or a slash is "required" for `BaseURL` and unset for `RatesBaseURL`. User info
  stays accepted: `net/http` sends it as `Authorization: Basic`, proved against a loopback server. Vectors
  `options.baseUrls`.
- **A `Config` in an unexported struct field prints its key** (G012-5): documented, not changed. `fmt` calls no
  method of a value it reaches through an unexported field, so `fmt.Sprintf("%+v", server{cfg})` shows `APIKey`.
  A key type of its own would hide it only if it held no string at all, which would break every
  `Config{APIKey: os.Getenv(...)}`. `Config`'s doc comment and the README say to keep the `*StreamClient` instead,
  which prints no secret wherever it sits; a test pins both.
- D9, checked: Go has no frame locals for an error reporter to read. Every error value of eight failure modes was
  walked by reflection, unexported fields and wrapped errors included: none holds the key or a URL password.
- Ids (D6): a string id has no nil, so there is nothing to refuse beyond `""`, `"."` and `".."`. Vectors
  `ids.numbers` are sent as their digits.
- Example `tokenserver` answers 502 to the SDK's refused 2xx, as it did to an empty token.
- The CHANGELOG shipped in `v0.1.2` read "prepared 2026-10-28, not published": 0.1.2 was published on 2026-10-28
  as tag `v0.1.2`.

## 0.1.2 (2026-10-28)

- The Music add-on in `GetRates`. `RtcRates.Addons` (nil for a price list from before the add-on) carries
  `Music`, an `RtcAddonRate`: `Price` in credits per 1,000 participant-minutes while music plays (`"10.00"`),
  `CuPerParticipantSecond` as the exact integer (`"1000"`), `Unit`, `AppliesTo` (`audio_std`, `audio_hd`: the
  voice tiers it is billed on top of), `IncludedIn` (the video tiers, which include it), `ID` and `Label`.
  Music is an add-on on standard and HD voice (₹10 per 1,000 participant-minutes while music plays, enabled in
  the project's Settings → Media and quality); included with video. `music` is an add-on id, not a tier: a
  usage line with `tier: "music"` is the add-on's participant-minutes next to the participant's own tier line.
  Pinned by the shared vectors (the `getRates` reply carries `addons.music`); `addons_test.go`. Nothing else
  changed.
- Docs only, no code change (the SDK red team of 2026-10-28): the README says how a reply is decoded.
  `encoding/json` matches a JSON key to a field case-insensitively, Unicode case folding included (`"PRICE"`
  fills `Price`, `"muſic"` fills `Music`; of two spellings the last wins), keeps the last value of a
  duplicated key and merges a duplicated object, drops a field the struct does not have (an add-on id newer
  than the release), and refuses a number in a string field. The other SDKs match keys exactly; a reply of
  the API holds each key once, in its exact case, so no real reply decodes differently. Pinned by the red
  team's `redteam_test.go`.

## 0.1.1 (2026-10-27)

- Reply size cap. A reply body is read up to 8 MiB and no further, on a 2xx
  and on an error status alike: a server that declares more (`Content-Length`)
  is refused before a byte of the body is read, and one that streams more is
  cut off one byte past the cap. The call fails with `http-<status>`, that
  status and the message `<METHOD> <path>: the reply is too large: over 8
  MiB, the most this SDK reads`; nothing of the reply is quoted, so nothing
  of it can leak. `Retryable` follows the status rule of a reply that named
  no code: a 5xx or 429 past the cap may pass, a 2xx or any other 4xx is not
  retried. A reply at or under the cap, error bodies included (the
  300-character cut and the redaction), is handled exactly as before. Until
  now a 2xx was read whole with `io.ReadAll`, so a misbehaving API or a proxy
  in front of it could force an allocation of any size. From the testers'
  hardening note of 2026-10-27; this SDK's own guard, the shared
  specification and the Node SDK read the whole reply. Tests in
  `replysize_test.go`.
- The CHANGELOG shipped in `v0.1.0` read "prepared 2026-10-23, not yet
  published": 0.1.0 was published on 2026-10-23 as tag `v0.1.0` (proxy.golang.org
  and pkg.go.dev indexed it the same day).
- Examples: a token endpoint answers 502 on an incomplete token reply; request bodies are capped at 1 MB.
  `examples/tokenserver` answers 502 with a JSON error when `CreateToken` returns no token or `wsUrl` (it
  handed on empty strings with a 200), logging the room and never the reply; it reads no body.
  `examples/webhookreceiver`, already capped at 1 MB, answers 413 instead of 400 to a larger body; its
  handler is now `receive(...)` so a test can call it. Both have tests (`go test ./examples/...`). No
  library change. From the testers' report of 2026-10-24 (EX-04 and EX-01, found in their own servers).

## 0.1.0 (2026-10-23, published the same day as tag `v0.1.0`)

The first release of the Go server SDK, at parity with `@voltkit-rtc/server`
0.4.8 (the shared specification `docs/SERVER-SDK-SPEC.md` and vectors
`docs/server-sdk-vectors.json` in the VoltKit RTC repository):

- Every method of the tenant REST API: rooms (`CreateRoom`, `EnsureRoom`,
  `CreateToken`, `GetRoom`, `ListRooms`, `ListParticipants`, `DeleteRoom`),
  chat (`CreateChatToken`, `GetChatInfo`, `ListMessages`, `SendMessage`,
  `DeleteMessage`, `ListChatMutes`, `MuteChat`, `UnmuteChat`), broadcast
  (`CreateStream`, `GetStream`, `ListStreams`, `UpdateStream`,
  `DeleteStream`, `CreateStreamToken`), usage (`GetUsage`,
  `GetUsageSeries`) and billing (`GetBalance`, `GetRates`), each with a
  `context.Context` first and typed request and reply structs.
- The retry policy of the Node SDK: jittered exponential backoff on network
  failures, 429s and 5xxs other than `billing-unavailable`, only on calls that
  are safe to repeat; `CreateRoom`, `CreateStream` and `SendMessage` sent
  once; deletes that lost their reply resolve when the retry finds the thing
  gone; a cancelled context stops the retries.
- One deadline per attempt covering headers and body; redirects never
  followed with the API key (`redirected`), followed only for the public
  rates; ids `""`, `"."` and `".."` refused before sending; bodies serialized
  once.
- `*Error` with `Code`, `Status`, `Message`, `Retryable` and the predicates
  `IsNotFound`, `IsOutOfMinutes`, `IsQuota`, `IsCapacityExhausted`,
  `IsBroadcastDisabled`, `IsBillingUnavailable`; the shared registry as
  `ErrorCodes`, `RESTErrorCodes` and `ErrorInfo`.
- The API key and URL credentials redacted from every message, in any
  encoding a reply can hold; the printed client (`%v`, `%+v`, `%#v`,
  `json.Marshal`, `log/slog`) shows no secret.
- Webhooks: `VerifyWebhook` and `ParseWebhook` over the raw body, with a
  freshness window, several `v1` signatures and a list of secrets for a
  rotation, compared in constant time.
- `User-Agent: voltkit-rtc-go/0.1.0` on every request; `NewStreamClientFromEnv`
  reading `VOLTKIT_RTC_URL`, `VOLTKIT_RTC_SECRET` and `VOLTKIT_API_URL`.
- Standard library only; Go 1.22 or newer.
- Reviewed adversarially before release (2026-10-23, the testers' catalogue
  run against the Go code paths): a `Config` and a client hide the key under
  every `fmt` verb (`Format`), in `encoding/json` and in `log/slog`, and the
  client's secrets live in closures reflection cannot read; an origin with a
  query or fragment is refused at startup; `Retries` at the top of `int` and
  the waits past the 25th retry no longer overflow; webhook freshness is
  computed on integers, so a 15-digit timestamp or a clock at the zero time is
  refused as the Node SDK refuses it, and the signed prefix is the timestamp
  exactly as the header spells it.
