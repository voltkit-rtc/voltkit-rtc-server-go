package voltkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultTimeout = 10 * time.Second
	defaultRetries = 2
	// maxTimeout is the longest deadline, 2^31-1 ms (about 24.8 days): the
	// longest a Node timer waits, kept so every SDK's deadline is the same
	// (spec section 2.2). Anything above it is held there.
	maxTimeout = 2147483647 * time.Millisecond
	// backoffBase is the wait before the first retry; each later wait doubles
	// it, and a uniform 0 to backoffJitter is added (spec section 4.1).
	backoffBase   = 200 * time.Millisecond
	backoffJitter = 100 * time.Millisecond
	// ratesPath is where the website API serves the public rates.
	ratesPath = "/api/v1/billing/rtc/rates/current"
)

// Config is what NewStreamClient needs. The zero value of an optional field
// means its default.
type Config struct {
	// BaseURL is the controller's origin, with its exact scheme and host:
	// https://api.voltkit.in for VoltKit cloud. The API key is sent here and
	// nowhere else. A trailing slash is dropped.
	BaseURL string
	// APIKey is the project API secret (sk_ and 64 hex digits). Server-side
	// only; whitespace around it is ignored. It is never quoted in an error.
	APIKey string
	// RatesBaseURL is the website API's origin (https://api.voltkit.in),
	// where GetRates reads the public price list with no credential. Only
	// GetRates needs it; "" and "/" mean unset.
	RatesBaseURL string
	// Timeout is the deadline for each attempt, from sending the request to
	// reading the last byte of the reply. Zero means 10 seconds; it must not
	// be negative. In whole milliseconds; above 2^31-1 ms it is held there.
	Timeout time.Duration
	// Retries is how many more attempts a call that is safe to repeat gets
	// after a transient failure. nil means 2; Ptr(0) turns retries off; it
	// must not be negative.
	Retries *int
	// HTTPClient is the transport: a client of your own for a proxy, custom
	// TLS or instrumentation. nil means a fresh http.Client on the default
	// transport. It is copied, never modified: the copy for the controller
	// never follows a redirect, the copy for the rates host keeps your
	// redirect policy, and both leave the deadline to the context.
	HTTPClient *http.Client
}

// String prints the configuration with the API key and any URL credentials
// hidden, so a Config in a log line or a panic message gives nothing away.
func (c Config) String() string {
	retries := "nil"
	if c.Retries != nil {
		retries = fmt.Sprint(*c.Retries)
	}
	key := "[redacted]"
	if c.APIKey == "" {
		key = `""`
	}
	return fmt.Sprintf("voltkit.Config{BaseURL: %q, APIKey: %s, RatesBaseURL: %q, Timeout: %s, Retries: %s}",
		redactURLCredentials(c.BaseURL), key, redactURLCredentials(c.RatesBaseURL), c.Timeout, retries)
}

// GoString is String: %#v shows no more than %v does.
func (c Config) GoString() string { return c.String() }

// Format prints the same redacted view under every fmt verb. Without it, a
// verb that has no meaning for a struct (%d, %t, %c, %U) makes fmt print each
// field as it is, key included. Config is still a plain struct: a
// reflection-based dumper, or %p on a value rather than a pointer (which go
// vet flags), reads its fields. Keep a Config out of dumps.
func (c Config) Format(f fmt.State, verb rune) { _, _ = io.WriteString(f, c.String()) }

// MarshalJSON serializes the redacted view, so a Config in a structured log
// line gives nothing away: zap, zerolog, logrus and slog's JSON handler all
// go through encoding/json.
func (c Config) MarshalJSON() ([]byte, error) {
	key := ""
	if c.APIKey != "" {
		key = redactedMarker
	}
	return json.Marshal(struct {
		BaseURL      string `json:"baseUrl"`
		APIKey       string `json:"apiKey"`
		RatesBaseURL string `json:"ratesBaseUrl,omitempty"`
		TimeoutMs    int64  `json:"timeoutMs"`
		Retries      *int   `json:"retries"`
	}{redactURLCredentials(c.BaseURL), key, redactURLCredentials(c.RatesBaseURL), c.Timeout.Milliseconds(), c.Retries})
}

// LogValue gives log/slog the redacted view as a group.
func (c Config) LogValue() slog.Value {
	key := ""
	if c.APIKey != "" {
		key = redactedMarker
	}
	attrs := []slog.Attr{
		slog.String("baseUrl", redactURLCredentials(c.BaseURL)),
		slog.String("apiKey", key),
		slog.String("ratesBaseUrl", redactURLCredentials(c.RatesBaseURL)),
		slog.Duration("timeout", c.Timeout),
	}
	if c.Retries != nil {
		attrs = append(attrs, slog.Int("retries", *c.Retries))
	}
	return slog.GroupValue(attrs...)
}

// secrets holds what must never be printed: the net key, the URLs as given
// (credentials included: requests go there) and the redactor built for the
// key. Each is a closure, as the Node SDK keeps them, because fmt prints a
// func as an address and reflection cannot see into one: whatever walks the
// client's fields (a fmt verb that has no meaning for a struct, a
// reflection-based dumper, a debugger's variable view) finds four addresses.
type secrets struct {
	apiKey       func() string
	baseURL      func() string
	ratesBaseURL func() string
	redactKey    func(text string) string
}

func newSecrets(apiKey, baseURL, ratesBaseURL string) *secrets {
	redactor := newKeyRedactor(apiKey)
	return &secrets{
		apiKey:       func() string { return apiKey },
		baseURL:      func() string { return baseURL },
		ratesBaseURL: func() string { return ratesBaseURL },
		redactKey:    redactor.redact,
	}
}

// redact returns text with the key, in any encoding, and any URL credentials
// replaced by [redacted]: for every message built from someone else's words.
func (s *secrets) redact(text string) string {
	return redactURLCredentials(s.redactKey(text))
}

// StreamClient is the client for the tenant REST API: rooms, tokens, chat,
// broadcast, usage, balance and rates. Make one with NewStreamClient or
// NewStreamClientFromEnv when your server starts and share it: it is safe for
// concurrent use and holds no state outside a call.
type StreamClient struct {
	s *secrets
	// baseURLShown and ratesBaseURLShown are the URLs with any credentials
	// as [redacted]: what printing the client shows.
	baseURLShown      string
	ratesBaseURLShown string
	timeout           time.Duration
	retries           int
	// keyed never follows a redirect; public (the rates host) may.
	keyed  *http.Client
	public *http.Client
	// sleep and jitter are the retry schedule's clock, replaced by tests.
	sleep  func(ctx context.Context, d time.Duration) error
	jitter func() time.Duration
}

// NewStreamClient makes a client. Every option is checked here, so a missing
// or malformed value fails at startup rather than on a user's first call, and
// none of these errors quotes the key.
func NewStreamClient(cfg Config) (*StreamClient, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("voltkit: Config.BaseURL is required")
	}
	if cfg.APIKey == "" {
		return nil, errors.New("voltkit: Config.APIKey is required")
	}
	apiKey, err := checkAPIKey(cfg.APIKey)
	if err != nil {
		return nil, err
	}
	baseURL := strings.TrimSuffix(cfg.BaseURL, "/")
	if err := checkOrigin("BaseURL", baseURL); err != nil {
		return nil, err
	}
	// Empty counts as unset, so GetRates says what is missing rather than
	// requesting a relative URL.
	ratesBaseURL := strings.TrimSuffix(cfg.RatesBaseURL, "/")
	if ratesBaseURL != "" {
		if err := checkOrigin("RatesBaseURL", ratesBaseURL); err != nil {
			return nil, err
		}
	}
	timeout := cfg.Timeout
	switch {
	case timeout == 0:
		timeout = defaultTimeout
	case timeout < 0:
		return nil, fmt.Errorf("voltkit: Config.Timeout must be above 0, got %s", timeout)
	case timeout > maxTimeout:
		timeout = maxTimeout
	}
	retries := defaultRetries
	if cfg.Retries != nil {
		if *cfg.Retries < 0 {
			return nil, fmt.Errorf("voltkit: Config.Retries must be 0 or more, got %d", *cfg.Retries)
		}
		retries = *cfg.Retries
	}
	base := cfg.HTTPClient
	if base == nil {
		base = &http.Client{}
	}
	// Two copies of the caller's client, which is left as it was. The keyed
	// copy treats a 3xx as the answer: Go's client would otherwise follow it,
	// and X-API-Key with it, to whatever host the Location names (section
	// 3.5). The deadline is the context's, not the client's: the context
	// covers the body read, and a client timeout would be a second clock.
	keyed := *base
	keyed.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	keyed.Timeout = 0
	public := *base
	public.Timeout = 0

	return &StreamClient{
		s:                 newSecrets(apiKey, baseURL, ratesBaseURL),
		baseURLShown:      redactURLCredentials(baseURL),
		ratesBaseURLShown: redactURLCredentials(ratesBaseURL),
		timeout:           timeout,
		retries:           retries,
		keyed:             &keyed,
		public:            &public,
		sleep:             sleepContext,
		jitter:            func() time.Duration { return rand.N(backoffJitter + 1) },
	}, nil
}

// NewStreamClientFromEnv makes a client from the environment: VOLTKIT_RTC_URL
// (BaseURL), VOLTKIT_RTC_SECRET (APIKey) and, optionally, VOLTKIT_API_URL
// (RatesBaseURL). Nothing else is read; for a timeout or retries, use
// NewStreamClient with a Config. A missing variable is an error naming it.
func NewStreamClientFromEnv() (*StreamClient, error) {
	baseURL := os.Getenv("VOLTKIT_RTC_URL")
	if baseURL == "" {
		return nil, errors.New("voltkit: VOLTKIT_RTC_URL is not set")
	}
	apiKey := os.Getenv("VOLTKIT_RTC_SECRET")
	if apiKey == "" {
		return nil, errors.New("voltkit: VOLTKIT_RTC_SECRET is not set")
	}
	return NewStreamClient(Config{
		BaseURL:      baseURL,
		APIKey:       apiKey,
		RatesBaseURL: os.Getenv("VOLTKIT_API_URL"),
	})
}

// checkAPIKey returns the net key, or an error that does not quote it (spec
// sections 2.2 and 2.3). Whitespace at both ends (space, tab, CR, LF) is
// dropped: a key read from a file often ends in a line break, and Node's fetch
// drops it too. A key no HTTP header can carry (a line break or NUL inside,
// another control character, DEL, anything above U+00FF) failed every call
// with the transport's own error, which quoted it. A backslash or a character
// U+0080 to U+00FF is one no VoltKit key has, and its echo in a reply could
// not be redacted reliably.
func checkAPIKey(key string) (string, error) {
	net := strings.Trim(key, " \t\r\n")
	if net == "" {
		return "", errors.New("voltkit: Config.APIKey is required: the one given is only whitespace")
	}
	for _, r := range net {
		if r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7F || r > 0xFF || r == utf8.RuneError {
			return "", errors.New("voltkit: Config.APIKey holds a character no HTTP header can carry: a line break, a NUL, another control character, or one above U+00FF. Check how the key is loaded (it is not shown here)")
		}
	}
	for _, r := range net {
		if r == '\\' || r >= 0x80 {
			return "", errors.New("voltkit: Config.APIKey holds a character no VoltKit key has: a backslash, or one outside ASCII such as an accented letter. Check how the key is loaded (it is not shown here)")
		}
	}
	return net, nil
}

// checkOrigin requires an absolute http or https URL with a host and no query
// or fragment: anything else would fail every call, far from the mistake (a
// path after "?" lands in the query, one after "#" is dropped). A path prefix
// is fine: a reverse proxy may well serve the API under one. The message never
// repeats the value, which may carry credentials.
func checkOrigin(field, origin string) error {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("voltkit: Config.%s must be an absolute http or https URL with a host, such as https://api.voltkit.in", field)
	}
	if strings.ContainsAny(origin, "?#") {
		return fmt.Errorf("voltkit: Config.%s must be an origin, with nothing after a \"?\" or \"#\": the request paths are appended to it", field)
	}
	return nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// --- printing: the client never shows its secrets (spec section 2.4) ------------

// String prints the client with URL credentials as [redacted] and no key:
// what %v and %+v show. Defined on the value, so a copied or dereferenced
// client prints the same way.
func (c StreamClient) String() string {
	rates := "unset"
	if c.ratesBaseURLShown != "" {
		rates = c.ratesBaseURLShown
	}
	return fmt.Sprintf("voltkit.StreamClient{BaseURL: %s, RatesBaseURL: %s, Timeout: %s, Retries: %d}",
		c.baseURLShown, rates, c.timeout, c.retries)
}

// GoString is String: %#v shows no more than %v does.
func (c StreamClient) GoString() string { return c.String() }

// Format prints the same redacted view under every fmt verb. fmt consults
// String only for %v, %s, %q, %x and %X; for a verb that has no meaning for a
// struct (%t, %c, %U, %f) it walks the fields instead and prints what it finds
// with %v, which, before this method and the closures in secrets, included the
// key.
func (c StreamClient) Format(f fmt.State, verb rune) { _, _ = io.WriteString(f, c.String()) }

// MarshalJSON serializes the same redacted view, so a client that ends up in
// a JSON log line or a debug dump gives nothing away.
func (c StreamClient) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		BaseURL      string `json:"baseUrl"`
		RatesBaseURL string `json:"ratesBaseUrl,omitempty"`
		TimeoutMs    int64  `json:"timeoutMs"`
		Retries      int    `json:"retries"`
	}{c.baseURLShown, c.ratesBaseURLShown, c.timeout.Milliseconds(), c.retries})
}

// LogValue gives log/slog the redacted view as a group.
func (c StreamClient) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("baseUrl", c.baseURLShown),
		slog.String("ratesBaseUrl", c.ratesBaseURLShown),
		slog.Duration("timeout", c.timeout),
		slog.Int("retries", c.retries),
	)
}

// --- transport -------------------------------------------------------------------

// call is one request as a method describes it.
type call struct {
	method string
	// path is the path as sent: ids encoded, query string included, no origin.
	path string
	// body is the request body, serialized once; hasBody says whether there
	// is one (GET, DELETE and PUT .../mutes/{identity} send none).
	body    any
	hasBody bool
	// safeToRepeat: whether a transient failure may be retried. Decided per
	// method, not per verb: a lost reply to a create would turn a success
	// into a spurious room-exists, and a retried sendMessage into a duplicate.
	safeToRepeat bool
	// goneIsDone: a delete. When an attempt failed in a way that may have
	// reached the server and done the work and a retry then answers 404, only
	// the earlier reply was lost (section 4.2).
	goneIsDone bool
	// public: send to the rates host, with no key, following redirects.
	public bool
}

// do runs a call: serializes the body once, makes the attempts the retry
// policy allows, and decodes a JSON reply into out (a pointer), which is left
// untouched by an empty reply. It returns the HTTP status of the reply that
// settled the call.
func (c *StreamClient) do(ctx context.Context, cl call, out any) (int, error) {
	// Once, before anything is sent: a body JSON cannot carry is the caller's
	// to fix, and no attempt would go differently. A plain error, not an
	// *Error: it is a mistake in the call, not an answer from VoltKit.
	var body []byte
	if cl.hasBody {
		var err error
		body, err = json.Marshal(cl.body)
		if err != nil {
			return 0, fmt.Errorf("%s %s: the body cannot be sent as JSON: %w", cl.method, cl.path, err)
		}
	}
	attempts := 1
	if cl.safeToRepeat {
		attempts = c.retries + 1
		if attempts <= 0 {
			// Retries at the top of int: the sum wrapped around. Any number
			// of attempts this large is "until it works or is refused".
			attempts = math.MaxInt
		}
	}
	var last *Error
	// Whether an earlier attempt may have reached the server and done the
	// work, its reply lost on the way back. A 429 is a refusal: that attempt
	// did nothing.
	mayHaveLanded := false
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			// Exponential with jitter: a fleet-wide blip should not produce a
			// synchronised retry from every backend at once. A cancelled
			// context ends the wait, and the call, with the context's error.
			if err := c.sleep(ctx, backoffWait(attempt)+c.jitter()); err != nil {
				return 0, err
			}
		}
		status, text, err := c.once(ctx, cl, body)
		if err == nil {
			if len(text) == 0 || out == nil {
				return status, nil
			}
			if err := json.Unmarshal(text, out); err != nil {
				// Valid JSON of another shape than the method's: something
				// other than the API answered, and asking again gets the same.
				return status, &Error{
					Code:      fmt.Sprintf("http-%d", status),
					Status:    status,
					Message:   fmt.Sprintf("%s %s: the reply could not be decoded: %s", cl.method, cl.path, c.s.redact(err.Error())),
					Retryable: false,
				}
			}
			return status, nil
		}
		var e *Error
		if !errors.As(err, &e) {
			// The caller's cancellation or deadline: the result is theirs, not
			// an *Error, and nothing is retried.
			return 0, err
		}
		if cl.goneIsDone && mayHaveLanded && isGone(e) {
			// The earlier attempt deleted it; this one finds it gone.
			return e.Status, nil
		}
		if !e.Retryable {
			return e.Status, e
		}
		last = e
		if e.Status != 429 {
			mayHaveLanded = true
		}
	}
	return last.Status, last
}

// backoffWait is the wait before retry n (1-based) without its jitter: 200 ms,
// doubling each time (200, 400, 800, 1600, ...; spec section 4.1), held at
// 2^31-1 ms, the deadline's cap, from the 25th retry on. Shifting further
// would overflow into a negative, which a timer treats as zero: an instant
// retry loop, exactly when the server has been failing for weeks.
func backoffWait(n int) time.Duration {
	if n > 24 {
		return maxTimeout
	}
	return min(backoffBase<<(n-1), maxTimeout)
}

// once makes one attempt under one deadline covering the whole exchange, from
// sending the request to the last byte of the body (spec section 3.4). It
// returns the status and the reply's text on a 2xx, an *Error for anything
// else, or the caller's context error.
func (c *StreamClient) once(ctx context.Context, cl call, body []byte) (int, []byte, error) {
	origin, client := c.s.baseURL(), c.keyed
	if cl.public {
		origin, client = c.s.ratesBaseURL(), c.public
	}
	attemptCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var reader io.Reader
	if cl.hasBody {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(attemptCtx, cl.method, origin+cl.path, reader)
	if err != nil {
		return 0, nil, c.transportError(ctx, attemptCtx, cl, err)
	}
	// The key is attached here and only here, and only for the controller.
	if !cl.public {
		req.Header.Set("X-API-Key", c.s.apiKey())
	}
	if cl.hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, c.transportError(ctx, attemptCtx, cl, err)
	}
	defer resp.Body.Close()

	if !cl.public && resp.StatusCode >= 300 && resp.StatusCode < 400 {
		// The 3xx itself is the answer: not retried (the same BaseURL
		// redirects the same way) and not followed. The target is named so
		// the fix is obvious, redacted and then cut, so the cut cannot leave
		// part of the key behind.
		to := ""
		if target := resp.Header.Get("Location"); target != "" {
			to = " to " + cutRunes(c.s.redact(target), 200)
		}
		return 0, nil, &Error{
			Code:      "redirected",
			Status:    resp.StatusCode,
			Message:   fmt.Sprintf("%s %s: the server answered %d%s; a request carrying the API key is never redirected. Point BaseURL at the controller itself (its exact scheme and host)", cl.method, cl.path, resp.StatusCode, to),
			Retryable: false,
		}
	}

	// Reading the body is network I/O too: a connection that drops after the
	// headers is the same transient failure as one that drops before them,
	// and the deadline covers it. Only the read is in here: a reply read in
	// full that then fails to parse is not a network error.
	text, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, c.transportError(ctx, attemptCtx, cl, err)
	}
	return c.parseReply(cl, resp.StatusCode, text)
}

// transportError classifies a failure in transit: the caller's own context
// first (their cancellation is the result), then the attempt's deadline, then
// the transport's words, redacted: they may quote the URL, credentials and
// all, or the header value they refused.
func (c *StreamClient) transportError(ctx, attemptCtx context.Context, cl call, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	reason := c.s.redact(err.Error())
	if attemptCtx.Err() == context.DeadlineExceeded {
		reason = fmt.Sprintf("timed out after %dms", c.timeout/time.Millisecond)
	}
	return &Error{Code: "network", Status: 0, Message: fmt.Sprintf("%s %s: %s", cl.method, cl.path, reason), Retryable: true}
}

// parseReply reads a whole reply (spec section 3.6): the text on a 2xx, an
// *Error otherwise. The server's words go through redaction before they reach
// a message, and before they are cut short.
func (c *StreamClient) parseReply(cl call, status int, text []byte) (int, []byte, error) {
	if status < 200 || status >= 300 {
		code, message := errorReply(text)
		if code == "" {
			code = fmt.Sprintf("http-%d", status)
		} else {
			// Redacted too: a server answering {"error": "Invalid API key:
			// <key>"} put the key in the code, the field a log line prints.
			code = c.s.redact(code)
		}
		if message == "" {
			message = cutRunes(c.s.redact(string(text)), 300)
		} else {
			message = c.s.redact(message)
		}
		// 5xx and 429 may pass; a 4xx is a decision the server made and will
		// make again. billing-unavailable is the 503 that will not pass: it
		// lasts until an operator activates a price list.
		retryable := (status >= 500 || status == 429) && code != "billing-unavailable"
		return status, nil, &Error{Code: code, Status: status, Message: fmt.Sprintf("%s %s: %s", cl.method, cl.path, message), Retryable: retryable}
	}
	if len(text) == 0 {
		return status, nil, nil
	}
	if !json.Valid(text) {
		// A success that is not JSON came from something other than the API
		// (a web page, a proxy) and a retry would fetch the same thing.
		return status, nil, &Error{
			Code:      fmt.Sprintf("http-%d", status),
			Status:    status,
			Message:   fmt.Sprintf("%s %s: the reply is not JSON: %s", cl.method, cl.path, cutRunes(c.s.redact(string(text)), 100)),
			Retryable: false,
		}
	}
	return status, text, nil
}

// errorReply reads the code and message of an error reply in either shape the
// two VoltKit APIs use: the controller's { "error": "<code>", "message": "<text>" }
// or the website API's { "success": false, "error": { "code", "message" } }.
// Empty strings count as absent; a body that is not a JSON object has neither.
func errorReply(text []byte) (code, message string) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(text, &top); err != nil {
		return "", ""
	}
	raw := bytes.TrimSpace(top["error"])
	if len(raw) > 0 && (raw[0] == '{' || raw[0] == '[') {
		var nested struct {
			Code    json.RawMessage `json:"code"`
			Message json.RawMessage `json:"message"`
		}
		_ = json.Unmarshal(raw, &nested)
		return nonEmptyString(nested.Code), nonEmptyString(nested.Message)
	}
	return nonEmptyString(raw), nonEmptyString(top["message"])
}

func nonEmptyString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// isGone is a 404 saying the thing is not there. Not one saying chat or
// broadcast is switched off on this deployment: that deleted nothing.
func isGone(e *Error) bool {
	return e.Status == 404 && e.Code != "chat-disabled" && e.Code != "broadcast-disabled"
}
