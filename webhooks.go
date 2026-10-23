package voltkit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Webhooks (spec section 7): VoltKit signs every delivery with
// X-Stream-Signature: t=<unix seconds>,v1=<hex>[,v1=<hex>...], where each v1
// is HMAC-SHA256(secret, "<t>." + raw body) in lower-case hex, one per secret
// during a rotation's overlap. VerifyWebhook and ParseWebhook check it over
// the exact bytes received.

// DefaultTolerance is how far out of date a delivery's timestamp may be,
// in either direction, when VerifyOptions.Tolerance is nil: five minutes. It
// guards against a captured request being replayed later.
const DefaultTolerance = 300 * time.Second

const maxSignatureHeaderLength = 4096

// VerifyOptions tunes VerifyWebhook and ParseWebhook. A nil *VerifyOptions is
// the defaults.
type VerifyOptions struct {
	// Tolerance is the freshness window, inclusive at the boundary. nil means
	// DefaultTolerance; Ptr(0) accepts only the current second; a negative
	// duration is a mistake in your setup (it would refuse every delivery).
	Tolerance *time.Duration
	// Now is the clock; nil means time.Now. For tests.
	Now func() time.Time
}

// VerifyWebhook reports whether a delivery is genuine and fresh: body is the
// request body exactly as received (never re-serialized), header the
// X-Stream-Signature value, secrets the signing secret and, during a rotation,
// the previous one too. Every secret is tried against every v1 value, each
// compared in constant time, and one match is enough.
//
// A mistake in the setup is loud, not a quiet false that would look like an
// attack: it panics when secrets is empty or holds only empty strings, or when
// Tolerance is negative, as the Node SDK throws a TypeError or RangeError. Use
// ParseWebhook to get those as errors instead.
func VerifyWebhook(body []byte, header string, secrets []string, opts *VerifyOptions) bool {
	ok, err := verifyWebhook(body, header, secrets, opts)
	if err != nil {
		panic(err)
	}
	return ok
}

// ParseWebhook verifies a delivery and decodes it in one step, so a handler
// cannot forget to check. A delivery that does not verify fails with an
// *Error of code bad-signature and status 401: answer 401, and VoltKit retries
// it. A mistake in the setup (no secret, a negative Tolerance, a verified body
// that is not JSON) is a plain error, never an *Error.
func ParseWebhook(body []byte, header string, secrets []string, opts *VerifyOptions) (*StreamEvent, error) {
	ok, err := verifyWebhook(body, header, secrets, opts)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &Error{Code: "bad-signature", Status: 401, Message: "webhook signature did not verify", Retryable: false}
	}
	var event StreamEvent
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, fmt.Errorf("voltkit: the webhook body is not JSON: %w", err)
	}
	return &event, nil
}

func verifyWebhook(body []byte, header string, secrets []string, opts *VerifyOptions) (bool, error) {
	var live []string
	for _, s := range secrets {
		if s != "" {
			live = append(live, s)
		}
	}
	if len(live) == 0 {
		return false, errors.New("voltkit: VerifyWebhook needs the signing secret")
	}
	tolerance := DefaultTolerance
	now := time.Now
	if opts != nil {
		if opts.Tolerance != nil {
			if *opts.Tolerance < 0 {
				return false, fmt.Errorf("voltkit: VerifyOptions.Tolerance must be 0 or more, got %s", *opts.Tolerance)
			}
			tolerance = *opts.Tolerance
		}
		if opts.Now != nil {
			now = opts.Now
		}
	}

	tsText, ts, signatures, ok := parseSignatureHeader(header)
	if !ok {
		return false, nil
	}
	// Freshness first, before any HMAC: both directions, inclusive.
	if !fresh(now(), ts, tolerance) {
		return false, nil
	}

	// Every secret against every signature, each compared in constant time.
	// Both lists are short (two at most in practice), and nothing returns
	// early on a partial match. The signed prefix is the timestamp as the
	// header spells it, which is what the sender signed (and what the Node
	// SDK verifies): a timestamp written with leading zeros is signed with
	// them.
	prefix := tsText + "."
	matched := false
	for _, secret := range live {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(prefix))
		mac.Write(body)
		expected := []byte(hex.EncodeToString(mac.Sum(nil)))
		for _, sig := range signatures {
			if hmac.Equal(expected, []byte(sig)) {
				matched = true
			}
		}
	}
	return matched, nil
}

// fresh reports whether a delivery stamped ts (unix seconds) is within
// tolerance of now, in either direction, inclusive at the boundary. Integer
// arithmetic rather than now.Sub(time.Unix(ts, 0)): a Duration saturates at
// about 292 years, and the negation of its minimum is itself, still negative,
// so with a 15-digit timestamp (31 million years ahead) or a clock at the
// zero time an abs() of the saturated difference would pass the comparison.
func fresh(now time.Time, ts int64, tolerance time.Duration) bool {
	// The most whole seconds a Duration holds, less one for the nanoseconds.
	const maxSeconds = int64(math.MaxInt64/int64(time.Second)) - 1
	seconds := now.Unix() - ts
	if seconds > maxSeconds || seconds < -maxSeconds {
		return false // beyond anything a tolerance can express
	}
	age := time.Duration(seconds)*time.Second + time.Duration(now.Nanosecond())
	if age < 0 {
		age = -age
	}
	return age <= tolerance
}

// parseSignatureHeader splits t=<ts>,v1=<hex>[,v1=<hex>...] into its timestamp
// (as written, and as a number) and signatures. At most 4096 characters; split
// on ","; each part trimmed; each must be key=value with a non-empty key; t
// must be 1 to 15 digits and appear exactly once; each v1 exactly 64
// lower-case hex characters; parts with other keys (v0=..., a future scheme)
// are skipped; anything else makes the header invalid.
func parseSignatureHeader(header string) (tsText string, ts int64, signatures []string, ok bool) {
	if len(header) > maxSignatureHeaderLength {
		return "", 0, nil, false
	}
	haveTS := false
	for _, part := range strings.Split(header, ",") {
		item := strings.TrimSpace(part)
		eq := strings.IndexByte(item, '=')
		if eq <= 0 {
			return "", 0, nil, false
		}
		key, value := item[:eq], item[eq+1:]
		switch key {
		case "t":
			if haveTS || !isDigits(value, 1, 15) {
				return "", 0, nil, false
			}
			haveTS = true
			tsText = value
			ts, _ = strconv.ParseInt(value, 10, 64)
		case "v1":
			if !isLowerHex(value, 64) {
				return "", 0, nil, false
			}
			signatures = append(signatures, value)
		}
	}
	if !haveTS || len(signatures) == 0 {
		return "", 0, nil, false
	}
	return tsText, ts, signatures, true
}

func isDigits(s string, min, max int) bool {
	if len(s) < min || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isLowerHex(s string, length int) bool {
	if len(s) != length {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(('0' <= c && c <= '9') || ('a' <= c && c <= 'f')) {
			return false
		}
	}
	return true
}
