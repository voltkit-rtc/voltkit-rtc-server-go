# Changelog

## 0.1.0 (prepared 2026-10-23, not yet published)

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
