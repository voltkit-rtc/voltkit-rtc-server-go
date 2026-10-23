package voltkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

// One test per behaviour of spec sections 2 to 6, against the fake transport.
// The shared vectors (vectors_test.go) repeat many of these case by case; here
// each behaviour is named and explained once.

// --- 2.1, 2.2: construction ----------------------------------------------------

func TestNewStreamClient_RequiresBaseURLAndKey(t *testing.T) {
	if _, err := NewStreamClient(Config{APIKey: testKey}); err == nil || !strings.Contains(err.Error(), "BaseURL is required") {
		t.Errorf("no BaseURL: %v", err)
	}
	if _, err := NewStreamClient(Config{BaseURL: "https://x.test"}); err == nil || !strings.Contains(err.Error(), "APIKey is required") {
		t.Errorf("no APIKey: %v", err)
	}
}

func TestNewStreamClient_RefusesKeysNoHeaderCanCarry(t *testing.T) {
	cases := []struct {
		name, key, reason string
	}{
		{"whitespace only", " \t\r\n ", "only whitespace"},
		{"a line feed inside", testKey + "\nX", "no HTTP header can carry"},
		{"a NUL", testKey + "\x00", "no HTTP header can carry"},
		{"DEL", testKey + "\x7f", "no HTTP header can carry"},
		{"above U+00FF", testKey + "Ā", "no HTTP header can carry"},
		{"an accented letter", "sk_live_café_0123456789abcdef", "no VoltKit key has"},
		{"a no-break space", "sk_live _0123456789abcdef", "no VoltKit key has"},
		{"a backslash", testKey + `\`, "no VoltKit key has"},
		{"a literal backslash-n from a quoted .env line", testKey + `\n`, "no VoltKit key has"},
	}
	for _, tc := range cases {
		_, err := NewStreamClient(Config{BaseURL: "https://x.test", APIKey: tc.key})
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.reason) {
			t.Errorf("%s: %v, want a message about %q", tc.name, err, tc.reason)
		}
		if strings.Contains(err.Error(), strings.TrimSpace(tc.key)) && strings.TrimSpace(tc.key) != "" {
			t.Errorf("%s: the message quotes the key", tc.name)
		}
		if strings.Contains(err.Error(), "0123456789abcdef") {
			t.Errorf("%s: the message quotes part of the key", tc.name)
		}
	}
}

func TestNewStreamClient_TrimsTheKeyAndSendsTheRest(t *testing.T) {
	// Node sends the key untrimmed and relies on fetch dropping the
	// whitespace; every other SDK trims itself (spec 2.3). A tab inside is kept.
	for _, tc := range []struct{ given, sent string }{
		{" " + testKey + "\r\n", testKey},
		{"\t" + testKey + "\n", testKey},
		{"sk_a\tb_0123456789abcdef", "sk_a\tb_0123456789abcdef"},
	} {
		ft := scripted(jsonStep(200, map[string]any{"rooms": []any{}}))
		c := newClient(t, ft, func(cfg *Config) { cfg.APIKey = tc.given })
		if _, err := c.ListRooms(ctxBG); err != nil {
			t.Fatal(err)
		}
		if got := ft.call(0).Headers.Get("X-API-Key"); got != tc.sent {
			t.Errorf("sent %q, want %q", got, tc.sent)
		}
	}
}

func TestNewStreamClient_TimeoutDefaultsCapsAndRefusals(t *testing.T) {
	c := newClient(t, scripted())
	if c.timeout != 10*time.Second {
		t.Errorf("default timeout %s, want 10s", c.timeout)
	}
	c = newClient(t, scripted(), func(cfg *Config) { cfg.Timeout = 1500 * time.Millisecond })
	if c.timeout != 1500*time.Millisecond {
		t.Errorf("timeout %s, want 1.5s", c.timeout)
	}
	// Past 2^31-1 ms (the longest a Node timer waits) the deadline is held
	// there, so every SDK's deadline is the same.
	c = newClient(t, scripted(), func(cfg *Config) { cfg.Timeout = 2147483648 * time.Millisecond })
	if c.timeout != 2147483647*time.Millisecond {
		t.Errorf("timeout %s, want the cap", c.timeout)
	}
	if _, err := NewStreamClient(Config{BaseURL: "https://x.test", APIKey: testKey, Timeout: -time.Millisecond}); err == nil || !strings.Contains(err.Error(), "Timeout must be above 0") {
		t.Errorf("negative timeout: %v", err)
	}
}

func TestNewStreamClient_RetriesDefaultsAndRefusals(t *testing.T) {
	ft := scripted(errorStep(503, "unavailable", "later"))
	if _, err := newClient(t, ft).GetBalance(ctxBG); err == nil || ft.count() != 3 {
		t.Errorf("default retries: %d attempts, want 3 (err %v)", ft.count(), err)
	}
	ft = scripted(errorStep(503, "unavailable", "later"))
	if _, err := newClient(t, ft, withRetries(0)).GetBalance(ctxBG); err == nil || ft.count() != 1 {
		t.Errorf("Retries 0: %d attempts, want 1", ft.count())
	}
	if _, err := NewStreamClient(Config{BaseURL: "https://x.test", APIKey: testKey, Retries: Ptr(-1)}); err == nil || !strings.Contains(err.Error(), "Retries must be 0 or more") {
		t.Errorf("negative retries: %v", err)
	}
}

func TestNewStreamClient_URLsTrailingSlashAndUnsetRates(t *testing.T) {
	ft := scripted(jsonStep(200, map[string]any{}), jsonStep(200, map[string]any{"success": true, "data": map[string]any{}}))
	c := newClient(t, ft, withRates("https://api.example.com/"))
	if _, err := c.ListRooms(ctxBG); err != nil {
		t.Fatal(err)
	}
	if got := ft.call(0).URL; got != "https://x.test/v1/rooms" {
		t.Errorf("rooms URL %q", got)
	}
	if _, err := c.GetRates(ctxBG); err != nil {
		t.Fatal(err)
	}
	if got := ft.call(1).URL; got != "https://api.example.com/api/v1/billing/rtc/rates/current" {
		t.Errorf("rates URL %q", got)
	}
	for _, unset := range []string{"", "/"} {
		c := newClient(t, scripted(), withRates(unset))
		_, err := c.GetRates(ctxBG)
		expectError(t, err, "rates-unconfigured", 0, false, "GetRates needs Config.RatesBaseURL: the website API origin that serves the public rates, e.g. https://api.example.com")
	}
}

func TestNewStreamClient_RefusesAnOriginThatIsNotAnHTTPURL(t *testing.T) {
	// Beyond the spec: Node accepts any non-empty string and fails every call
	// with the transport's words. Failing at startup is kinder, and the
	// message never repeats the value, which may carry credentials.
	for _, bad := range []string{"x.test", "ftp://x.test", "https://", "https://user:pw@"} {
		_, err := NewStreamClient(Config{BaseURL: bad, APIKey: testKey})
		if err == nil || !strings.Contains(err.Error(), "BaseURL must be an absolute http or https URL") {
			t.Errorf("%q accepted or wrong error: %v", bad, err)
		}
		if strings.Contains(err.Error(), "pw") {
			t.Errorf("%q: the message repeats the URL", bad)
		}
	}
	if _, err := NewStreamClient(Config{BaseURL: "https://x.test", APIKey: testKey, RatesBaseURL: "api.example.com"}); err == nil || !strings.Contains(err.Error(), "RatesBaseURL must be") {
		t.Errorf("bad RatesBaseURL: %v", err)
	}
}

func TestNewStreamClient_CopiesTheHTTPClient(t *testing.T) {
	given := &http.Client{Transport: scripted(), Timeout: 3 * time.Second}
	c := newClient(t, nil, func(cfg *Config) { cfg.HTTPClient = given })
	if given.CheckRedirect != nil || given.Timeout != 3*time.Second {
		t.Error("the caller's client was modified")
	}
	if c.keyed == given || c.public == given {
		t.Error("the client uses the caller's http.Client itself, not a copy")
	}
	if c.keyed.CheckRedirect == nil || c.keyed.Timeout != 0 || c.public.Timeout != 0 {
		t.Error("the copies must refuse redirects (keyed) and leave the deadline to the context")
	}
	if c.keyed.Transport != given.Transport {
		t.Error("the copies must keep the caller's transport")
	}
}

func TestNewStreamClientFromEnv(t *testing.T) {
	t.Setenv("VOLTKIT_RTC_URL", "")
	t.Setenv("VOLTKIT_RTC_SECRET", "")
	t.Setenv("VOLTKIT_API_URL", "")
	if _, err := NewStreamClientFromEnv(); err == nil || !strings.Contains(err.Error(), "VOLTKIT_RTC_URL") {
		t.Errorf("no URL: %v", err)
	}
	t.Setenv("VOLTKIT_RTC_URL", "https://x.test")
	if _, err := NewStreamClientFromEnv(); err == nil || !strings.Contains(err.Error(), "VOLTKIT_RTC_SECRET") {
		t.Errorf("no secret: %v", err)
	}
	t.Setenv("VOLTKIT_RTC_SECRET", testKey)
	t.Setenv("VOLTKIT_API_URL", "https://api.example.com/")
	c, err := NewStreamClientFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.s.baseURL() != "https://x.test" || c.s.ratesBaseURL() != "https://api.example.com" || c.s.apiKey() != testKey {
		t.Errorf("env not read: %s", c)
	}
}

// --- 2.4: the client never shows its secrets --------------------------------------

func TestClient_PrintingHidesTheKeyAndURLCredentials(t *testing.T) {
	ft := scripted(jsonStep(200, map[string]any{"rooms": []any{}}))
	key := "sk_live_Zm9v+YmFyLzk/QmF6+cXV4/dGVzdF9rZXk+MTIz/NDU2Nzg=="
	c := newClient(t, ft, func(cfg *Config) {
		cfg.APIKey = key
		cfg.BaseURL = "https://svc:hunter2@x.test/"
		cfg.RatesBaseURL = "https://web:s3cret@api.example.com"
	})
	var views []string
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		views = append(views, fmt.Sprintf(verb, c), fmt.Sprintf(verb, *c))
	}
	j, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	views = append(views, string(j))
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("client", "client", c)
	views = append(views, buf.String())
	views = append(views, fmt.Sprintf("%v %+v %#v", Config{BaseURL: "https://svc:hunter2@x.test/", APIKey: key}, Config{APIKey: key}, Config{APIKey: key}))

	for _, v := range views {
		for _, secret := range []string{key, "hunter2", "s3cret", "Zm9v"} {
			if strings.Contains(v, secret) {
				t.Errorf("%q shows %q", v, secret)
			}
		}
	}
	if !strings.Contains(views[0], "https://[redacted]@x.test") || !strings.Contains(views[0], "https://[redacted]@api.example.com") {
		t.Errorf("the URLs must be shown with their credentials as [redacted]: %s", views[0])
	}
	if !strings.Contains(string(j), `"baseUrl":"https://[redacted]@x.test"`) {
		t.Errorf("json: %s", j)
	}
	// Requests still go to the URL as given, credentials included.
	if _, err := c.ListRooms(ctxBG); err != nil {
		t.Fatal(err)
	}
	if got := ft.call(0).URL; got != "https://svc:hunter2@x.test/v1/rooms" {
		t.Errorf("request URL %q", got)
	}
}

// --- 3.2: headers ------------------------------------------------------------------

func TestRequest_HeadersOnKeyedCalls(t *testing.T) {
	ft := scripted(jsonStep(200, map[string]any{}))
	c := newClient(t, ft)
	_, _ = c.CreateToken(ctxBG, CreateTokenParams{RoomID: "r", Identity: "u", Role: RoleViewer})
	_, _ = c.ListRooms(ctxBG)
	_ = c.DeleteRoom(ctxBG, "r")
	_ = c.MuteChat(ctxBG, "c", "u")
	post, get, del, put := ft.call(0), ft.call(1), ft.call(2), ft.call(3)
	for _, r := range []recordedCall{post, get, del, put} {
		if r.Headers.Get("X-API-Key") != testKey {
			t.Errorf("%s %s: X-API-Key %q", r.Method, r.Path, r.Headers.Get("X-API-Key"))
		}
		if r.Headers.Get("User-Agent") != "voltkit-rtc-go/"+Version {
			t.Errorf("%s %s: User-Agent %q", r.Method, r.Path, r.Headers.Get("User-Agent"))
		}
		if r.Headers.Get("Authorization") != "" {
			t.Errorf("%s %s: an Authorization header", r.Method, r.Path)
		}
	}
	if post.Headers.Get("Content-Type") != "application/json" || !post.HasBody {
		t.Errorf("POST: Content-Type %q, body %v", post.Headers.Get("Content-Type"), post.HasBody)
	}
	for _, r := range []recordedCall{get, del, put} {
		if r.Headers.Get("Content-Type") != "" || r.HasBody {
			t.Errorf("%s %s: must send no body and no Content-Type", r.Method, r.Path)
		}
	}
}

func TestRequest_RatesSendsNoCredential(t *testing.T) {
	ft := scripted(jsonStep(200, map[string]any{"success": true, "data": map[string]any{"version": 3}}))
	c := newClient(t, ft, withRates("https://api.example.com"))
	rates, err := c.GetRates(ctxBG)
	if err != nil {
		t.Fatal(err)
	}
	if rates.Version != 3 {
		t.Errorf("rates %+v", rates)
	}
	h := ft.call(0).Headers
	if h.Get("X-API-Key") != "" || h.Get("Authorization") != "" {
		t.Errorf("the rates host was sent a credential: %v", h)
	}
	if h.Get("User-Agent") != "voltkit-rtc-go/"+Version {
		t.Errorf("User-Agent %q", h.Get("User-Agent"))
	}
}

// --- 3.3: bodies -------------------------------------------------------------------

func TestRequest_BodySerializedOnceAndResentIdentically(t *testing.T) {
	ft := scripted(errorStep(503, "unavailable", "later"), errorStep(503, "unavailable", "later"), jsonStep(200, map[string]any{"roomId": "r"}))
	c := newClient(t, ft)
	_, err := c.EnsureRoom(ctxBG, EnsureRoomParams{RoomID: "r", HostIdentity: "h", Metadata: map[string]any{"seats": 8}})
	if err != nil {
		t.Fatal(err)
	}
	if ft.count() != 3 {
		t.Fatalf("%d attempts", ft.count())
	}
	want := `{"hostIdentity":"h","metadata":{"seats":8}}`
	for i := 0; i < 3; i++ {
		if got := string(ft.call(i).Body); got != want {
			t.Errorf("attempt %d body %s, want %s", i+1, got, want)
		}
	}
}

func TestRequest_OptionalFieldsAreLeftOut(t *testing.T) {
	ft := scripted(jsonStep(201, map[string]any{}))
	c := newClient(t, ft)
	_, _ = c.CreateRoom(ctxBG, CreateRoomParams{HostIdentity: "h"})
	_, _ = c.SendMessage(ctxBG, SendMessageParams{Channel: "c", Text: "hi"})
	_, _ = c.CreateChatToken(ctxBG, CreateChatTokenParams{Channel: "c", Identity: "u"})
	_, _ = c.CreateStreamToken(ctxBG, CreateStreamTokenParams{StreamID: "s"})
	for i, want := range []string{`{"hostIdentity":"h"}`, `{"text":"hi"}`, `{"identity":"u"}`, `{}`} {
		if got := string(ft.call(i).Body); got != want {
			t.Errorf("body %d: %s, want %s (no nulls, no path ids)", i, got, want)
		}
	}
}

func TestRequest_ABodyJSONCannotCarryFailsBeforeSending(t *testing.T) {
	ft := scripted(jsonStep(200, map[string]any{}))
	c := newClient(t, ft)
	_, err := c.EnsureRoom(ctxBG, EnsureRoomParams{RoomID: "show-42", HostIdentity: "h", Metadata: make(chan int)})
	if err == nil {
		t.Fatal("accepted a channel as metadata")
	}
	var e *Error
	if errors.As(err, &e) {
		t.Errorf("a programmer error must be a plain error, not *Error: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "PUT /v1/rooms/show-42: the body cannot be sent as JSON: ") {
		t.Errorf("message %q", err)
	}
	if ft.count() != 0 {
		t.Errorf("%d requests were sent", ft.count())
	}
}

// --- 3.1: ids ----------------------------------------------------------------------

func TestIDs_DotsAndEmptyRefusedBeforeSending(t *testing.T) {
	ft := scripted(jsonStep(200, map[string]any{}))
	c := newClient(t, ft)
	for _, id := range []string{"", ".", ".."} {
		err := c.DeleteRoom(ctxBG, id)
		expectError(t, err, "bad-request", 0, false, fmt.Sprintf("roomId %q: an id may not be \"\", \".\" or \"..\", which a URL resolves to another endpoint; nothing was sent", id))
		_, err = c.CreateStreamToken(ctxBG, CreateStreamTokenParams{StreamID: id})
		expectError(t, err, "bad-request", 0, false, "")
		err = c.UnmuteChat(ctxBG, "lobby", id)
		expectError(t, err, "bad-request", 0, false, fmt.Sprintf("identity %q: an id may not be \"\", \".\" or \"..\", which a URL resolves to another endpoint; nothing was sent", id))
	}
	if ft.count() != 0 {
		t.Errorf("%d requests were sent", ft.count())
	}
}

func TestIDs_EncodedAsOneSegmentByTheEncodeURIComponentRule(t *testing.T) {
	for id, want := range map[string]string{
		"show-42": "show-42", "...": "...", "a b/c?d#e": "a%20b%2Fc%3Fd%23e", "%2e%2e": "%252e%252e",
		"ü-房间": "%C3%BC-%E6%88%BF%E9%97%B4", "!*'()": "!*'()", "~tilde": "~tilde", "a+b": "a%2Bb",
		"a&b=c": "a%26b%3Dc", "user@host": "user%40host", "x\x00y": "x%00y", "a\\b": "a%5Cb",
	} {
		if got := encodeSegment(id); got != want {
			t.Errorf("encodeSegment(%q) = %q, want %q", id, got, want)
		}
	}
	ft := scripted(jsonStep(200, map[string]any{}))
	c := newClient(t, ft)
	_, _ = c.GetRoom(ctxBG, "a b/c")
	if got := ft.call(0).URL; got != "https://x.test/v1/rooms/a%20b%2Fc" {
		t.Errorf("URL %q", got)
	}
	_, _ = c.ListMessages(ctxBG, "show 42", &ListMessagesOptions{Limit: 20, Before: "a b*~"})
	if got := ft.call(1).Path; got != "/v1/chat/show%2042/messages?limit=20&before=a+b*%7E" {
		t.Errorf("query %q (form-encoded, in the order given)", got)
	}
	_, _ = c.ListMessages(ctxBG, "c", &ListMessagesOptions{})
	if got := ft.call(2).Path; got != "/v1/chat/c/messages" {
		t.Errorf("empty options must add no query string: %q", got)
	}
}

// --- 3.4: the deadline ---------------------------------------------------------------

func TestDeadline_ANeverEndingReplyFailsWithTheTimeoutAndIsRetriedOnlyWhenSafe(t *testing.T) {
	ft := scripted(step{hangs: true})
	c := newClient(t, ft, func(cfg *Config) { cfg.Timeout = 30 * time.Millisecond }, withRetries(1))
	_, err := c.ListRooms(ctxBG)
	expectError(t, err, "network", 0, true, "GET /v1/rooms: timed out after 30ms")
	if ft.count() != 2 {
		t.Errorf("%d attempts, want 2: a timeout is transient and ListRooms is safe to repeat", ft.count())
	}
	_, err = c.SendMessage(ctxBG, SendMessageParams{Channel: "c", Text: "hi"})
	expectError(t, err, "network", 0, true, "POST /v1/chat/c/messages: timed out after 30ms")
	if ft.count() != 3 {
		t.Errorf("SendMessage must be sent once: %d", ft.count()-2)
	}
}

func TestDeadline_TheCallersContextWinsAndIsNotAnError(t *testing.T) {
	ft := scripted(step{hangs: true})
	c := newClient(t, ft, func(cfg *Config) { cfg.Timeout = time.Hour })
	ctx, cancel := context.WithTimeout(ctxBG, 20*time.Millisecond)
	defer cancel()
	_, err := c.ListRooms(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want the caller's context.DeadlineExceeded, got %v", err)
	}
	var e *Error
	if errors.As(err, &e) {
		t.Errorf("the caller's deadline must not be reported as an *Error")
	}
	if ft.count() != 1 {
		t.Errorf("%d attempts: a cancelled call is not retried", ft.count())
	}
}

func TestDeadline_ABodyThatBreaksOffIsNetworkAndRetried(t *testing.T) {
	ft := scripted(step{bodyDropped: true}, jsonStep(200, map[string]any{"rooms": []any{map[string]any{"roomId": "r"}}}))
	list, err := newClient(t, ft).ListRooms(ctxBG)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Rooms) != 1 || ft.count() != 2 {
		t.Errorf("rooms %v after %d attempts", list.Rooms, ft.count())
	}
	ft = scripted(step{bodyDropped: true})
	_, err = newClient(t, ft).GetBalance(ctxBG)
	e := expectError(t, err, "network", 0, true, "")
	if !strings.HasPrefix(e.Message, "GET /v1/billing/balance: ") || ft.count() != 3 {
		t.Errorf("%q after %d attempts", e.Message, ft.count())
	}
}

// --- 3.5: redirects --------------------------------------------------------------------

func TestRedirect_NeverFollowedWithTheKeyOnAnyVerb(t *testing.T) {
	ft := scripted(step{status: 302, headers: map[string]string{"Location": "https://elsewhere.test/v1/billing/balance"}})
	c := newClient(t, ft)
	calls := []struct {
		name string
		do   func() error
		path string
	}{
		{"POST", func() error { _, err := c.CreateRoom(ctxBG, CreateRoomParams{HostIdentity: "h"}); return err }, "POST /v1/rooms"},
		{"PUT", func() error {
			_, err := c.EnsureRoom(ctxBG, EnsureRoomParams{RoomID: "r", HostIdentity: "h"})
			return err
		}, "PUT /v1/rooms/r"},
		{"GET", func() error { _, err := c.GetBalance(ctxBG); return err }, "GET /v1/billing/balance"},
		{"DELETE", func() error { return c.DeleteRoom(ctxBG, "r") }, "DELETE /v1/rooms/r"},
		{"PATCH", func() error { _, err := c.UpdateStream(ctxBG, "s", UpdateStreamParams{}); return err }, "PATCH /v1/streams/s"},
		{"PUT mute", func() error { return c.MuteChat(ctxBG, "c", "u") }, "PUT /v1/chat/c/mutes/u"},
	}
	for i, tc := range calls {
		err := tc.do()
		expectError(t, err, "redirected", 302, false, tc.path+": the server answered 302 to https://elsewhere.test/v1/billing/balance; a request carrying the API key is never redirected. Point BaseURL at the controller itself (its exact scheme and host)")
		if ft.count() != i+1 {
			t.Errorf("%s: %d requests after %d calls: a redirect is neither followed nor retried", tc.name, ft.count(), i+1)
		}
	}
	ft = scripted(step{status: 308})
	err := newClient(t, ft).DeleteRoom(ctxBG, "r")
	expectError(t, err, "redirected", 308, false, "DELETE /v1/rooms/r: the server answered 308; a request carrying the API key is never redirected. Point BaseURL at the controller itself (its exact scheme and host)")
}

func TestRedirect_TheRatesHostMayBeFollowed(t *testing.T) {
	ft := scripted(
		step{status: 302, headers: map[string]string{"Location": "https://www.api.example.com/api/v1/billing/rtc/rates/current"}},
		jsonStep(200, map[string]any{"success": true, "data": map[string]any{"version": 7}}),
	)
	rates, err := newClient(t, ft, withRates("https://api.example.com")).GetRates(ctxBG)
	if err != nil {
		t.Fatal(err)
	}
	if rates.Version != 7 || ft.count() != 2 || ft.call(1).URL != "https://www.api.example.com/api/v1/billing/rtc/rates/current" {
		t.Errorf("version %d after %d requests, second to %q", rates.Version, ft.count(), ft.call(1).URL)
	}
	if ft.call(1).Headers.Get("X-API-Key") != "" {
		t.Error("the key went to the redirect target")
	}
}

// --- 3.6: reading the reply --------------------------------------------------------------

func TestReply_EmptyBodyResolvesToNothing(t *testing.T) {
	ft := scripted(step{status: 200})
	c := newClient(t, ft)
	if err := c.DeleteRoom(ctxBG, "r"); err != nil {
		t.Error(err)
	}
	if err := c.MuteChat(ctxBG, "c", "u"); err != nil {
		t.Error(err)
	}
	// A typed result from an empty 2xx stays zero rather than failing.
	ft = scripted(step{status: 204})
	people, err := newClient(t, ft).ListParticipants(ctxBG, "r")
	if err != nil || len(people) != 0 {
		t.Errorf("%v, %v", people, err)
	}
}

func TestReply_ASuccessThatIsNotJSONIsHTTPStatusNotRetried(t *testing.T) {
	ft := scripted(rawStep(200, "<html>proxy</html>"))
	_, err := newClient(t, ft).ListRooms(ctxBG)
	expectError(t, err, "http-200", 200, false, "GET /v1/rooms: the reply is not JSON: <html>proxy</html>")
	if ft.count() != 1 {
		t.Errorf("%d attempts: the same page would come back", ft.count())
	}
}

func TestReply_ErrorShapesOfBothVoltKitAPIs(t *testing.T) {
	cases := []struct {
		step    step
		code    string
		message string
		retry   bool
	}{
		{errorStep(404, "no-room", "room not found"), "no-room", "GET /v1/rooms/r: room not found", false},
		{jsonStep(500, map[string]any{"success": false, "error": map[string]any{"code": "INTERNAL_ERROR", "message": "boom"}}), "INTERNAL_ERROR", "GET /v1/rooms/r: boom", true},
		{jsonStep(500, map[string]any{"success": false, "error": map[string]any{"message": "boom"}}), "http-500", "GET /v1/rooms/r: boom", true},
		{jsonStep(500, map[string]any{"error": "", "message": "boom"}), "http-500", "GET /v1/rooms/r: boom", true},
		{jsonStep(404, map[string]any{"error": "no-room", "message": ""}), "no-room", `GET /v1/rooms/r: {"error":"no-room","message":""}`, false},
		{rawStep(500, `"oops"`), "http-500", `GET /v1/rooms/r: "oops"`, true},
		{rawStep(502, "<html>502 Bad Gateway</html>"), "http-502", "GET /v1/rooms/r: <html>502 Bad Gateway</html>", true},
		{errorStep(429, "rate-limited", "slow down"), "rate-limited", "GET /v1/rooms/r: slow down", true},
		{errorStep(503, "billing-unavailable", "no price list"), "billing-unavailable", "GET /v1/rooms/r: no price list", false},
		{errorStep(503, "unavailable", "later"), "unavailable", "GET /v1/rooms/r: later", true},
	}
	for _, tc := range cases {
		_, err := newClient(t, scripted(tc.step), withRetries(0)).GetRoom(ctxBG, "r")
		expectError(t, err, tc.code, tc.step.status, tc.retry, tc.message)
	}
	// A reply with no message field shows the whole body, cut at 300 after redaction.
	long := strings.Repeat("x", 400)
	_, err := newClient(t, scripted(rawStep(503, long)), withRetries(0)).ListStreams(ctxBG)
	expectError(t, err, "http-503", 503, true, "GET /v1/streams: "+long[:300])
}

func TestReply_ValidJSONOfAnotherShapeIsHTTPStatusNotRetried(t *testing.T) {
	// Beyond the spec: Node hands back whatever JSON.parse gave; a typed SDK
	// cannot, so it reports the decode failure as a non-retryable http-200.
	ft := scripted(rawStep(200, `"a string where a room was expected"`))
	_, err := newClient(t, ft).GetRoom(ctxBG, "r")
	e := expectError(t, err, "http-200", 200, false, "")
	if !strings.HasPrefix(e.Message, "GET /v1/rooms/r: the reply could not be decoded: ") || ft.count() != 1 {
		t.Errorf("%q after %d attempts", e.Message, ft.count())
	}
}

// --- 4.1: retries -------------------------------------------------------------------------

func TestRetry_ScheduleIsExponentialFrom200msAndTheLastErrorIsReported(t *testing.T) {
	ft := scripted(errorStep(503, "unavailable", "first"), errorStep(500, "internal", "second"), rawStep(502, "third"), errorStep(503, "unavailable", "fourth"), errorStep(503, "unavailable", "fifth"))
	c := newClient(t, ft, withRetries(4))
	sleeps := pinClock(c)
	_, err := c.GetBalance(ctxBG)
	expectError(t, err, "unavailable", 503, true, "GET /v1/billing/balance: fifth")
	if ft.count() != 5 {
		t.Errorf("%d attempts, want 5", ft.count())
	}
	want := []time.Duration{200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond}
	if fmt.Sprint(*sleeps) != fmt.Sprint(want) {
		t.Errorf("waits %v, want %v", *sleeps, want)
	}
}

func TestRetry_JitterIsUniformUpTo100ms(t *testing.T) {
	c, err := NewStreamClient(Config{BaseURL: "https://x.test", APIKey: testKey})
	if err != nil {
		t.Fatal(err)
	}
	var over, zero int
	for i := 0; i < 2000; i++ {
		j := c.jitter()
		if j < 0 || j > 100*time.Millisecond {
			t.Fatalf("jitter %s outside 0..100ms", j)
		}
		if j > 50*time.Millisecond {
			over++
		}
		if j == 0 {
			zero++
		}
	}
	if over < 500 || over > 1500 || zero > 10 {
		t.Errorf("jitter does not look uniform: %d of 2000 above 50ms, %d zero", over, zero)
	}
}

func TestRetry_TheSecondOrThirdAttemptWins(t *testing.T) {
	ft := scripted(step{network: true}, errorStep(503, "unavailable", "later"), jsonStep(200, map[string]any{"token": "t"}))
	tok, err := newClient(t, ft).CreateToken(ctxBG, CreateTokenParams{RoomID: "r", Identity: "u", Role: RoleViewer})
	if err != nil || tok.Token != "t" || ft.count() != 3 {
		t.Errorf("%v %v after %d attempts", tok, err, ft.count())
	}
}

func TestRetry_NeverForCreatesAndSends_ButReportedRetryable(t *testing.T) {
	for name, do := range map[string]func(c *StreamClient) error{
		"CreateRoom": func(c *StreamClient) error {
			_, err := c.CreateRoom(ctxBG, CreateRoomParams{HostIdentity: "h"})
			return err
		},
		"CreateStream": func(c *StreamClient) error {
			_, err := c.CreateStream(ctxBG, CreateStreamParams{StreamID: "s"})
			return err
		},
		"SendMessage": func(c *StreamClient) error {
			_, err := c.SendMessage(ctxBG, SendMessageParams{Channel: "c", Text: "hi"})
			return err
		},
	} {
		for _, st := range []step{errorStep(503, "unavailable", "later"), {network: true}, {bodyDropped: true}, errorStep(429, "rate-limited", "slow")} {
			ft := scripted(st, jsonStep(201, map[string]any{}))
			e := asError(t, do(newClient(t, ft)))
			if !e.Retryable || ft.count() != 1 {
				t.Errorf("%s on %+v: retryable %v, %d attempts", name, st, e.Retryable, ft.count())
			}
		}
	}
}

func TestRetry_NotForDecisionsTheServerMade(t *testing.T) {
	for _, st := range []step{errorStep(404, "no-room", "x"), errorStep(409, "room-exists", "x"), errorStep(401, "unauthorized", "x"), errorStep(400, "bad-request", "x"), errorStep(402, "quota-exhausted", "x"), errorStep(403, "tenant-suspended", "x"), {status: 302, headers: map[string]string{"Location": "https://e.test"}}, rawStep(200, "<html>")} {
		ft := scripted(st, jsonStep(200, map[string]any{}))
		if _, err := newClient(t, ft).EnsureRoom(ctxBG, EnsureRoomParams{RoomID: "r", HostIdentity: "h"}); err == nil || ft.count() != 1 {
			t.Errorf("%+v: err %v after %d attempts", st, err, ft.count())
		}
	}
}

func TestRetry_StopsWhenTheCallerCancels(t *testing.T) {
	ft := scripted(errorStep(503, "unavailable", "later"))
	c := newClient(t, ft)
	ctx, cancel := context.WithCancel(ctxBG)
	c.sleep = func(ctx context.Context, d time.Duration) error {
		cancel() // the caller gives up while the SDK waits to retry
		return ctx.Err()
	}
	_, err := c.GetBalance(ctx)
	if !errors.Is(err, context.Canceled) || ft.count() != 1 {
		t.Errorf("err %v after %d attempts; want context.Canceled after 1", err, ft.count())
	}
}

// --- 4.2: deletes that lost their reply -----------------------------------------------------

func TestGoneIsDone_OnlyAfterAnAttemptThatMayHaveLanded(t *testing.T) {
	// Lost, then 404: the first attempt did the deleting.
	ft := scripted(step{network: true}, errorStep(404, "no-room", "not found"))
	if err := newClient(t, ft).DeleteRoom(ctxBG, "r"); err != nil || ft.count() != 2 {
		t.Errorf("lost then 404: %v after %d", err, ft.count())
	}
	// A 5xx may have landed too.
	ft = scripted(rawStep(502, "<html>502</html>"), errorStep(404, "no-stream", "gone"))
	if err := newClient(t, ft).DeleteStream(ctxBG, "s"); err != nil {
		t.Errorf("502 then 404: %v", err)
	}
	// A 429 is a refusal that did nothing: a 404 after it is not found.
	ft = scripted(errorStep(429, "rate-limited", "slow"), errorStep(404, "no-room", "not found"))
	expectError(t, newClient(t, ft).DeleteRoom(ctxBG, "r"), "no-room", 404, false, "DELETE /v1/rooms/r: not found")
	// A 404 on the first attempt is not found.
	ft = scripted(errorStep(404, "no-message", "not found"))
	expectError(t, newClient(t, ft).DeleteMessage(ctxBG, "lobby", "1-0"), "no-message", 404, false, "DELETE /v1/chat/lobby/messages/1-0: not found")
	// A 404 saying the feature is off deleted nothing.
	ft = scripted(step{network: true}, errorStep(404, "chat-disabled", "off"))
	expectError(t, newClient(t, ft).UnmuteChat(ctxBG, "lobby", "u"), "chat-disabled", 404, false, "")
	// Only the four deletes: a GET that was lost then 404 is not found.
	ft = scripted(step{network: true}, errorStep(404, "no-room", "not found"))
	_, err := newClient(t, ft).GetRoom(ctxBG, "r")
	expectError(t, err, "no-room", 404, false, "GET /v1/rooms/r: not found")
	// A different refusal on the retry is reported as itself.
	ft = scripted(errorStep(503, "unavailable", "later"), errorStep(401, "unauthorized", "no"))
	expectError(t, newClient(t, ft).DeleteRoom(ctxBG, "r"), "unauthorized", 401, false, "DELETE /v1/rooms/r: no")
}

// --- 5.1, 5.2: the error ---------------------------------------------------------------------

func TestError_FieldsPredicatesAndErrorsAs(t *testing.T) {
	ft := scripted(errorStep(503, "capacity-exhausted", "every media node is over threshold"))
	_, err := newClient(t, ft, withRetries(0)).CreateRoom(ctxBG, CreateRoomParams{HostIdentity: "h"})
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("not an *Error: %v", err)
	}
	if e.Code != "capacity-exhausted" || e.Status != 503 || !e.Retryable || e.Message != "POST /v1/rooms: every media node is over threshold" || err.Error() != e.Message {
		t.Errorf("%+v", e)
	}
	if !e.IsCapacityExhausted() || e.IsQuota() || e.IsNotFound() || e.IsOutOfMinutes() || e.IsBroadcastDisabled() || e.IsBillingUnavailable() {
		t.Errorf("predicates on %+v", e)
	}
	var nilErr *Error
	if nilErr.IsNotFound() || nilErr.IsQuota() || nilErr.Error() != "<nil>" {
		t.Error("the predicates must be nil-safe")
	}
	if fmt.Sprint(wrapped(e)) != "wrapped: "+e.Message || !errors.As(wrapped(e), &e) {
		t.Error("errors.As must find an *Error through a wrapper")
	}
}

func wrapped(e error) error { return fmt.Errorf("wrapped: %w", e) }

func TestErrorInfo_ReadsTheRegistry(t *testing.T) {
	info, ok := ErrorInfo("billing-unavailable")
	if !ok || info.Origin != OriginServer || !info.Terminal || info.Retryable {
		t.Errorf("%+v %v", info, ok)
	}
	if _, ok := ErrorInfo("http-502"); ok {
		t.Error("http-<status> is outside the registry")
	}
	if len(ErrorCodes) != 105 || len(RESTErrorCodes) != 29 || ErrorCodes[0] != "bad-json" || RESTErrorCodes[0] != "internal" {
		t.Errorf("%d codes, %d REST codes", len(ErrorCodes), len(RESTErrorCodes))
	}
	for _, code := range RESTErrorCodes {
		if _, ok := ErrorInfo(code); !ok {
			t.Errorf("REST code %s is not in the registry", code)
		}
	}
}

// --- 6: methods -------------------------------------------------------------------------------

func TestMethods_ListsUnwrapAndAreEmptyWhenAbsent(t *testing.T) {
	ft := scripted(jsonStep(200, map[string]any{}))
	c := newClient(t, ft)
	people, _ := c.ListParticipants(ctxBG, "r")
	messages, _ := c.ListMessages(ctxBG, "c", nil)
	mutes, _ := c.ListChatMutes(ctxBG, "c")
	streams, _ := c.ListStreams(ctxBG)
	series, _ := c.GetUsageSeries(ctxBG, 7)
	for name, got := range map[string]any{"participants": people, "messages": messages, "mutes": mutes, "streams": streams, "series": series} {
		j, _ := json.Marshal(got)
		if string(j) != "[]" {
			t.Errorf("%s: %s, want [] (never null)", name, j)
		}
	}
	rooms, err := c.ListRooms(ctxBG)
	if err != nil || rooms == nil {
		t.Errorf("ListRooms keeps the wrapper object: %v %v", rooms, err)
	}
}

func TestMethods_EnsureRoomReportsWhetherTheRoomExisted(t *testing.T) {
	created, _ := newClient(t, scripted(jsonStep(201, map[string]any{"roomId": "r", "hostToken": "t"}))).EnsureRoom(ctxBG, EnsureRoomParams{RoomID: "r", HostIdentity: "h"})
	existing, _ := newClient(t, scripted(jsonStep(200, map[string]any{"roomId": "r", "hostToken": "t2"}))).EnsureRoom(ctxBG, EnsureRoomParams{RoomID: "r", HostIdentity: "h"})
	if created.Existed || !existing.Existed || existing.HostToken != "t2" {
		t.Errorf("created %+v existing %+v", created, existing)
	}
	room, _ := newClient(t, scripted(jsonStep(201, map[string]any{"roomId": "r", "existed": true}))).CreateRoom(ctxBG, CreateRoomParams{HostIdentity: "h"})
	if room.Existed {
		t.Error("CreateRoom always reports existed false")
	}
}

func TestMethods_UsageSeriesDefaultsTo30Days(t *testing.T) {
	ft := scripted(jsonStep(200, map[string]any{"series": []any{}}))
	c := newClient(t, ft)
	_, _ = c.GetUsageSeries(ctxBG, 0)
	_, _ = c.GetUsageSeries(ctxBG, 7)
	if ft.call(0).Path != "/v1/usage/series?days=30" || ft.call(1).Path != "/v1/usage/series?days=7" {
		t.Errorf("%q %q", ft.call(0).Path, ft.call(1).Path)
	}
}

func TestMethods_GetRatesChecksTheEnvelope(t *testing.T) {
	_, err := newClient(t, scripted(jsonStep(200, map[string]any{"status": "ok"})), withRates("https://api.example.com")).GetRates(ctxBG)
	expectError(t, err, "http-200", 200, false, "GET /api/v1/billing/rtc/rates/current: the reply carries no rates; is Config.RatesBaseURL the website API origin?")
	_, err = newClient(t, scripted(jsonStep(200, map[string]any{"success": true, "data": []any{}})), withRates("https://api.example.com")).GetRates(ctxBG)
	expectError(t, err, "http-200", 200, false, "")
	_, err = newClient(t, scripted(jsonStep(429, map[string]any{"success": false, "error": map[string]any{"code": "RATE_LIMITED", "message": "Too many requests."}})), withRates("https://api.example.com"), withRetries(0)).GetRates(ctxBG)
	expectError(t, err, "RATE_LIMITED", 429, true, "GET /api/v1/billing/rtc/rates/current: Too many requests.")
}

func TestMethods_TypedResultsKeepMetadataAndMoneyAsGiven(t *testing.T) {
	ft := scripted(jsonStep(200, map[string]any{
		"tenantId": "acme", "billingMode": "wallet", "canJoin": false, "blocked": "quota-exhausted",
		"wallet":         map[string]any{"availableCu": "123456789012345678901234567890", "paidCu": "1", "promoCu": "2", "spendOrder": []any{"project:acme"}, "scopes": map[string]any{}},
		"pricingVersion": 3, "burn": map[string]any{"projectCuPerSecond": "9900"},
	}))
	b, err := newClient(t, ft).GetBalance(ctxBG)
	if err != nil {
		t.Fatal(err)
	}
	if b.Blocked == nil || *b.Blocked != "quota-exhausted" || b.Wallet == nil || b.Wallet.AvailableCu != "123456789012345678901234567890" || b.PricingVersion == nil || *b.PricingVersion != 3 {
		t.Errorf("%+v", b)
	}
	cu, err := ParseCu(b.Wallet.AvailableCu)
	if err != nil || cu.String() != "123456789012345678901234567890" {
		t.Errorf("ParseCu: %v %v", cu, err)
	}
	if _, err := ParseCu("12.5"); err == nil {
		t.Error("ParseCu must refuse a decimal fraction")
	}
	ft = scripted(jsonStep(200, map[string]any{"room": map[string]any{"roomId": "r", "metadata": map[string]any{"seats": 8, "nested": []any{1, "two"}}}, "participants": []any{}}))
	view, err := newClient(t, ft).GetRoom(ctxBG, "r")
	if err != nil || string(view.Room.Metadata) != `{"nested":[1,"two"],"seats":8}` {
		t.Errorf("metadata %s %v", view.Room.Metadata, err)
	}
}
