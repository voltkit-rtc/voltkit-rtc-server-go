package voltkit

// Error is what every failed call returns (the Node SDK's StreamError, spec
// section 5.1). Use errors.As to reach it:
//
//	var e *voltkit.Error
//	if errors.As(err, &e) && e.IsNotFound() { ... }
//
// The controller answers errors as { "error": "<code>", "message": "<text>" }
// with a meaningful status. Collapsing those into a generic error loses the
// one thing a caller needs to decide what to do: capacity-exhausted means page
// someone and retry, room-quota means your own cleanup is behind, bad-request
// means fix the call. So the code is kept, and Message is always safe to log:
// the API key and any URL credentials are redacted before they can reach it.
type Error struct {
	// Code is the server's code (no-room, quota-exhausted, ...), network when
	// no reply arrived, http-<status> when a reply named no code or could not
	// be used (not JSON, another shape, or over the 8 MiB reply cap),
	// redirected, rates-unconfigured, or bad-request for an id refused before
	// sending. A newer controller may send a code this release does not know;
	// it is passed through unchanged.
	Code string
	// Status is the HTTP status: 0 when nothing reached the server or the SDK
	// refused the call itself, the 3xx for redirected.
	Status int
	// Message reads "<METHOD> <path>: <what went wrong>", with the path as
	// sent (ids encoded, query string included).
	Message string
	// Retryable is true when trying again later could work: network, a 429, or
	// a 5xx other than billing-unavailable. On a call the SDK repeats it means
	// the retries have already been spent (section 4.1).
	Retryable bool
}

// Error returns Message, so the generic error surface shows the method, the
// path and the server's words.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Message
}

// IsNotFound reports a 404: the room, stream or message is gone, or was never
// yours (rooms are tenant-scoped, so those are the same answer).
// broadcast-disabled and chat-disabled are 404s too: test IsBroadcastDisabled
// first when it matters.
func (e *Error) IsNotFound() bool { return e != nil && e.Status == 404 }

// IsOutOfMinutes reports that the account cannot use more right now: a 402,
// or the codes quota-exhausted, daily-limit-reached and monthly-limit-reached.
func (e *Error) IsOutOfMinutes() bool {
	return e != nil && (e.Status == 402 || e.Code == "quota-exhausted" ||
		e.Code == "daily-limit-reached" || e.Code == "monthly-limit-reached")
}

// IsQuota reports room-quota or any 429 (rate-limited, viewer-quota and
// stream-quota included): slow down, or end rooms you no longer need.
func (e *Error) IsQuota() bool { return e != nil && (e.Code == "room-quota" || e.Status == 429) }

// IsCapacityExhausted reports capacity-exhausted or no-nodes: the fleet is
// full. This is the one to alert on; it is about capacity, not correctness.
func (e *Error) IsCapacityExhausted() bool {
	return e != nil && (e.Code == "capacity-exhausted" || e.Code == "no-nodes")
}

// IsBroadcastDisabled reports broadcast-disabled: broadcast is not switched on
// for this deployment. Not something a retry fixes.
func (e *Error) IsBroadcastDisabled() bool { return e != nil && e.Code == "broadcast-disabled" }

// IsBillingUnavailable reports billing-unavailable: the project is billed from
// its wallet and no price list is active yet, or the controller has no wallet
// billing. An operator fixes this, not a retry, so although it is a 503 the
// SDK never retries it.
func (e *Error) IsBillingUnavailable() bool { return e != nil && e.Code == "billing-unavailable" }

// Origin says who raises a registry code: the server (the controller or a
// media node, passed through unchanged by every SDK) or an SDK itself.
type Origin string

const (
	// OriginServer marks a code the controller or a media node sends.
	OriginServer Origin = "server"
	// OriginSDK marks a code an SDK raises on its own (network, redirected).
	OriginSDK Origin = "sdk"
)

// CodeInfo is one entry of the shared error registry (docs/sdk-contract.json
// in the VoltKit RTC repository): what ErrorInfo returns.
type CodeInfo struct {
	// Code is the registry code, as Error.Code carries it.
	Code string
	// Origin is who raises it: the server, or an SDK itself.
	Origin Origin
	// Terminal concerns client SDKs: a join refused with this code must not
	// be retried with the same token. Nothing in this SDK acts on it.
	Terminal bool
	// Retryable is the registry's flag: trying the same call again later can
	// succeed. Error.Retryable on a failed call follows the HTTP status rule
	// of section 3.6 and agrees with this for every REST code.
	Retryable bool
}

// registry is every error code a VoltKit RTC SDK can report, in the order of
// the shared registry, with its flags. A copy rather than a read of the
// contract file: the module ships without the repository's docs, and
// scripts/test/sdk-contract-sdks.mjs fails the build when the two drift
// apart. One code per line, in this exact form, so that script can read it.
var registry = []CodeInfo{
	{Code: "bad-json", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "bad-params", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "unknown-method", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "too-many-requests", Origin: OriginServer, Terminal: false, Retryable: true},
	{Code: "not-joined", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "already-joined", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "internal", Origin: OriginServer, Terminal: false, Retryable: true},
	{Code: "node-error", Origin: OriginServer, Terminal: false, Retryable: true},
	{Code: "no-token", Origin: OriginServer, Terminal: true, Retryable: false},
	{Code: "bad-token", Origin: OriginServer, Terminal: true, Retryable: false},
	{Code: "wrong-tenant", Origin: OriginServer, Terminal: true, Retryable: false},
	{Code: "token-mismatch", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "no-room", Origin: OriginServer, Terminal: true, Retryable: false},
	{Code: "room-ended", Origin: OriginServer, Terminal: true, Retryable: false},
	{Code: "kicked", Origin: OriginServer, Terminal: true, Retryable: false},
	{Code: "no-node", Origin: OriginServer, Terminal: false, Retryable: true},
	{Code: "no-capacity", Origin: OriginServer, Terminal: false, Retryable: true},
	{Code: "stage-full", Origin: OriginServer, Terminal: false, Retryable: true},
	{Code: "participant-quota", Origin: OriginServer, Terminal: false, Retryable: true},
	{Code: "viewer-quota", Origin: OriginServer, Terminal: false, Retryable: true},
	{Code: "quota-exhausted", Origin: OriginServer, Terminal: true, Retryable: false},
	{Code: "daily-limit-reached", Origin: OriginServer, Terminal: true, Retryable: false},
	{Code: "monthly-limit-reached", Origin: OriginServer, Terminal: true, Retryable: false},
	{Code: "tenant-suspended", Origin: OriginServer, Terminal: true, Retryable: false},
	{Code: "billing-unavailable", Origin: OriginServer, Terminal: true, Retryable: false},
	{Code: "forbidden", Origin: OriginServer, Terminal: true, Retryable: false},
	{Code: "not-in-room", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "no-participant", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "bad-transport", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "no-transport", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "no-producer", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "no-consumer", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "cannot-consume", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "consumer-cap", Origin: OriginServer, Terminal: false, Retryable: true},
	{Code: "producer-cap", Origin: OriginServer, Terminal: false, Retryable: true},
	{Code: "room-cap", Origin: OriginServer, Terminal: false, Retryable: true},
	{Code: "draining", Origin: OriginServer, Terminal: false, Retryable: true},
	{Code: "bad-request", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "source-already-published", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "force-muted", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "media-disabled", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "frame-rate-not-allowed", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "video-quality-not-allowed", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "audio-quality-not-allowed", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "video-not-allowed", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "audio-not-allowed", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "video-subscription-limit", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "slot-taken", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "slot-blocked", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "slot-occupied", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "chat-muted", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "rate-limited", Origin: OriginServer, Terminal: false, Retryable: true},
	{Code: "no-message", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "chat-unavailable", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "closed", Origin: OriginServer, Terminal: false, Retryable: true},
	{Code: "unauthorized", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "bad-signature", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "operator-only", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "bad-role", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "room-exists", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "room-quota", Origin: OriginServer, Terminal: false, Retryable: true},
	{Code: "no-nodes", Origin: OriginServer, Terminal: false, Retryable: true},
	{Code: "capacity-exhausted", Origin: OriginServer, Terminal: false, Retryable: true},
	{Code: "unavailable", Origin: OriginServer, Terminal: false, Retryable: true},
	{Code: "not-found", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "broadcast-disabled", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "chat-disabled", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "no-stream", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "stream-exists", Origin: OriginServer, Terminal: false, Retryable: false},
	{Code: "stream-quota", Origin: OriginServer, Terminal: false, Retryable: true},
	{Code: "network", Origin: OriginSDK, Terminal: false, Retryable: true},
	{Code: "rates-unconfigured", Origin: OriginSDK, Terminal: false, Retryable: false},
	{Code: "redirected", Origin: OriginSDK, Terminal: false, Retryable: false},
	{Code: "not-connected", Origin: OriginSDK, Terminal: false, Retryable: true},
	{Code: "timeout", Origin: OriginSDK, Terminal: false, Retryable: true},
	{Code: "connect-failed", Origin: OriginSDK, Terminal: false, Retryable: true},
	{Code: "replaced", Origin: OriginSDK, Terminal: true, Retryable: false},
	{Code: "reconnect-gave-up", Origin: OriginSDK, Terminal: true, Retryable: false},
	{Code: "devices-gone", Origin: OriginSDK, Terminal: false, Retryable: false},
	{Code: "unpublished", Origin: OriginSDK, Terminal: false, Retryable: false},
	{Code: "frame-rate-fallback", Origin: OriginSDK, Terminal: false, Retryable: false},
	{Code: "codec-fallback", Origin: OriginSDK, Terminal: false, Retryable: false},
	{Code: "quality-unavailable", Origin: OriginSDK, Terminal: false, Retryable: false},
	{Code: "screen-share-active", Origin: OriginSDK, Terminal: false, Retryable: false},
	{Code: "screen-share-unsupported", Origin: OriginSDK, Terminal: false, Retryable: false},
	{Code: "screen-share-no-video", Origin: OriginSDK, Terminal: false, Retryable: false},
	{Code: "disconnected", Origin: OriginSDK, Terminal: false, Retryable: true},
	{Code: "produce-failed", Origin: OriginSDK, Terminal: false, Retryable: true},
	{Code: "consume-failed", Origin: OriginSDK, Terminal: false, Retryable: true},
	{Code: "media-unavailable", Origin: OriginSDK, Terminal: false, Retryable: false},
	{Code: "music-unavailable", Origin: OriginSDK, Terminal: false, Retryable: false},
	{Code: "no-camera", Origin: OriginSDK, Terminal: false, Retryable: false},
	{Code: "camera-failed", Origin: OriginSDK, Terminal: false, Retryable: true},
	{Code: "unknown-producer", Origin: OriginSDK, Terminal: false, Retryable: false},
	{Code: "screen-share-denied", Origin: OriginSDK, Terminal: false, Retryable: true},
	{Code: "screen-share-service-required", Origin: OriginSDK, Terminal: false, Retryable: false},
	{Code: "screen-share-source-required", Origin: OriginSDK, Terminal: false, Retryable: false},
	{Code: "screen-share-setup-required", Origin: OriginSDK, Terminal: false, Retryable: false},
	{Code: "screen-share-start-timeout", Origin: OriginSDK, Terminal: false, Retryable: true},
	{Code: "screen-share-ended", Origin: OriginSDK, Terminal: false, Retryable: true},
	{Code: "screen-share-failed", Origin: OriginSDK, Terminal: false, Retryable: true},
	{Code: "screen-share-size-unavailable", Origin: OriginSDK, Terminal: false, Retryable: true},
	{Code: "screen-audio-unavailable", Origin: OriginSDK, Terminal: false, Retryable: false},
	{Code: "screen-stop-failed", Origin: OriginSDK, Terminal: false, Retryable: false},
	{Code: "degradation-preference-failed", Origin: OriginSDK, Terminal: false, Retryable: false},
}

// ErrorCodes is every code in the shared registry, in its order. Most of
// these only ever reach a client SDK on its socket; RESTErrorCodes are the
// ones a call from this SDK can fail with. Treat the slice as read-only.
var ErrorCodes = func() []string {
	codes := make([]string, len(registry))
	for i, info := range registry {
		codes[i] = info.Code
	}
	return codes
}()

// RESTErrorCodes is the registry codes whose surfaces include the tenant REST
// API, in the registry's order: what a call from this SDK can fail with,
// besides http-<status> for a reply that names no code and, from GetRates,
// the website API's own codes (RATE_LIMITED, NOT_FOUND, ...), which are passed
// through unchanged. Treat the slice as read-only.
var RESTErrorCodes = []string{
	"internal",
	"no-room",
	"viewer-quota",
	"quota-exhausted",
	"daily-limit-reached",
	"monthly-limit-reached",
	"tenant-suspended",
	"billing-unavailable",
	"bad-request",
	"rate-limited",
	"no-message",
	"unauthorized",
	"bad-signature",
	"operator-only",
	"bad-role",
	"room-exists",
	"room-quota",
	"no-nodes",
	"capacity-exhausted",
	"unavailable",
	"not-found",
	"broadcast-disabled",
	"chat-disabled",
	"no-stream",
	"stream-exists",
	"stream-quota",
	"network",
	"rates-unconfigured",
	"redirected",
}

var registryByCode = func() map[string]CodeInfo {
	m := make(map[string]CodeInfo, len(registry))
	for _, info := range registry {
		m[info.Code] = info
	}
	return m
}()

// ErrorInfo looks a code up in the shared registry: its origin and its
// terminal and retryable flags. The second value is false for a code outside
// the registry, such as http-<status> or one a newer controller sends.
func ErrorInfo(code string) (CodeInfo, bool) {
	info, ok := registryByCode[code]
	return info, ok
}
