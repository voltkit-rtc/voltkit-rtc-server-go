# Changelog

## 0.1.2 (prepared 2026-10-28, not published)

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
