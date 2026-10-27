package voltkit

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// The adversarial review (brief item 10): the testers' attacks of the last
// eight rounds (the Node test IDs NODE-01/02, NODE-041-1..5, N043..N047) run
// against the Go code paths, plus what Go adds of its own: fmt verbs,
// encoding/json and log/slog on every exported type, context cancellation,
// Go's redirect cap, time.Duration saturation, integer overflow, and servers
// that misbehave on the wire in ways a fake transport cannot.

// --- helpers --------------------------------------------------------------------

// rawServer speaks TCP, not HTTP: for replies net/http's server cannot be made
// to send (a connection closed mid-headers, a malformed status line, HTTP/1.0).
func rawServer(t *testing.T, serve func(conn net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				serve(conn)
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return "http://" + ln.Addr().String()
}

// readRequestHead consumes the request line and headers, so the reply is
// written to a client that has finished sending.
func readRequestHead(conn net.Conn) {
	br := bufio.NewReader(conn)
	for {
		line, err := br.ReadString('\n')
		if err != nil || line == "\r\n" || line == "\n" {
			return
		}
	}
}

func within(t *testing.T, started time.Time, limit time.Duration, what string) {
	t.Helper()
	if elapsed := time.Since(started); elapsed > limit {
		t.Errorf("%s took %s, the deadline must hold", what, elapsed)
	}
}

// --- (c) the deadline under servers that never finish ------------------------------

func TestAdversarial_RepliesThatNeverFinishFailAtTheDeadline(t *testing.T) {
	flush := func(w http.ResponseWriter) { w.(http.Flusher).Flush() }
	cases := []struct {
		name    string
		handler func(w http.ResponseWriter, r *http.Request)
	}{
		{"nothing at all after the request", func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}},
		{"headers only, then nothing", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			flush(w)
			<-r.Context().Done()
		}},
		{"one byte every 40 ms", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			doc := `{"rooms":[{"roomId":"r"}]}`
			for i := 0; ; i++ {
				if _, err := w.Write([]byte{doc[i%len(doc)]}); err != nil {
					return
				}
				flush(w)
				select {
				case <-r.Context().Done():
					return
				case <-time.After(40 * time.Millisecond):
				}
			}
		}},
		{"chunked, never ending", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			chunk := []byte(`{"rooms":[` + strings.Repeat(`{"roomId":"r"},`, 60))
			for {
				if _, err := w.Write(chunk); err != nil {
					return
				}
				flush(w)
				select {
				case <-r.Context().Done():
					return
				case <-time.After(20 * time.Millisecond):
				}
			}
		}},
		{"a Content-Length far larger than what is sent, then silence", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1000000")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"rooms":`))
			flush(w)
			<-r.Context().Done()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(tc.handler))
			defer srv.Close()
			c := liveClient(t, srv.URL, func(cfg *Config) { cfg.Timeout = 200 * time.Millisecond }, withRetries(0))
			started := time.Now()
			_, err := c.ListRooms(ctxBG)
			expectError(t, err, "network", 0, true, "GET /v1/rooms: timed out after 200ms")
			within(t, started, 1500*time.Millisecond, tc.name)
		})
	}
}

func TestAdversarial_BrokenAndOddRepliesOnTheWire(t *testing.T) {
	t.Run("the connection closes in the middle of the headers", func(t *testing.T) {
		u := rawServer(t, func(conn net.Conn) {
			readRequestHead(conn)
			_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: appl")
		})
		started := time.Now()
		_, err := liveClient(t, u, withRetries(0)).ListRooms(ctxBG)
		e := expectError(t, err, "network", 0, true, "")
		if !strings.HasPrefix(e.Message, "GET /v1/rooms: ") {
			t.Errorf("%q", e.Message)
		}
		within(t, started, 2*time.Second, "closed mid-headers")
	})
	t.Run("a malformed status line that echoes the key and a URL with credentials is redacted", func(t *testing.T) {
		u := rawServer(t, func(conn net.Conn) {
			readRequestHead(conn)
			_, _ = io.WriteString(conn, "BOGUS "+testKey+" at https://ops:hunter2@proxy.test/x\r\n\r\n")
		})
		_, err := liveClient(t, u, withRetries(0)).ListRooms(ctxBG)
		e := expectError(t, err, "network", 0, true, "")
		if strings.Contains(e.Message, testKey) || strings.Contains(e.Message, "hunter2") || !strings.Contains(e.Message, "[redacted]") {
			t.Errorf("%q", e.Message)
		}
	})
	t.Run("an HTTP/1.0 reply without Content-Length is read to the end", func(t *testing.T) {
		u := rawServer(t, func(conn net.Conn) {
			readRequestHead(conn)
			_, _ = io.WriteString(conn, "HTTP/1.0 200 OK\r\nContent-Type: application/json\r\n\r\n{\"rooms\":[{\"roomId\":\"r\"}]}")
		})
		list, err := liveClient(t, u, withRetries(0)).ListRooms(ctxBG)
		if err != nil || len(list.Rooms) != 1 {
			t.Errorf("%v %v", list, err)
		}
	})
	t.Run("a gzip claim over a body that is not gzip is a network error, not a hang or a panic", func(t *testing.T) {
		srv := serve(t, func(w http.ResponseWriter, r *http.Request, _ int) {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"rooms":[]}`))
		})
		started := time.Now()
		_, err := liveClient(t, srv.URL, withRetries(1)).ListRooms(ctxBG)
		e := expectError(t, err, "network", 0, true, "")
		if !strings.HasPrefix(e.Message, "GET /v1/rooms: ") || srv.hits.Load() != 2 {
			t.Errorf("%q after %d hits", e.Message, srv.hits.Load())
		}
		within(t, started, 2*time.Second, "bad gzip")
	})
	t.Run("a genuinely gzipped reply is decoded", func(t *testing.T) {
		srv := serve(t, func(w http.ResponseWriter, r *http.Request, _ int) {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", "application/json")
			gz := gzip.NewWriter(w)
			_, _ = gz.Write([]byte(`{"rooms":[{"roomId":"r"}]}`))
			_ = gz.Close()
		})
		list, err := liveClient(t, srv.URL).ListRooms(ctxBG)
		if err != nil || len(list.Rooms) != 1 {
			t.Errorf("%v %v", list, err)
		}
	})
}

func TestAdversarial_NoGoroutineLeaksAfterStalledBrokenAndCancelledCalls(t *testing.T) {
	before := runtime.NumGoroutine()
	func() {
		stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { stallBody(w, r, 0) }))
		defer stalled.Close()
		broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"rooms":`))
			w.(http.Flusher).Flush()
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				conn.Close()
			}
		}))
		defer broken.Close()
		c := liveClient(t, stalled.URL, func(cfg *Config) { cfg.Timeout = 80 * time.Millisecond }, withRetries(1))
		for i := 0; i < 4; i++ {
			_, _ = c.ListRooms(ctxBG)
		}
		c2 := liveClient(t, broken.URL)
		for i := 0; i < 4; i++ {
			_, _ = c2.ListRooms(ctxBG)
		}
		ctx, cancel := context.WithCancel(ctxBG)
		time.AfterFunc(30*time.Millisecond, cancel)
		_, _ = liveClient(t, stalled.URL, func(cfg *Config) { cfg.Timeout = time.Hour }).ListRooms(ctx)
	}()
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	after := runtime.NumGoroutine()
	for i := 0; i < 80 && after > before+2; i++ {
		time.Sleep(50 * time.Millisecond)
		after = runtime.NumGoroutine()
	}
	if after > before+2 {
		buf := make([]byte, 1<<16)
		t.Errorf("goroutines before %d, after %d: something leaked\n%s", before, after, buf[:runtime.Stack(buf, true)])
	}
}

// --- (d) cancellation ---------------------------------------------------------------

func TestAdversarial_CancellationMidBodyIsTheCallersResultAndNotRetried(t *testing.T) {
	srv := serve(t, stallBody)
	c := liveClient(t, srv.URL, func(cfg *Config) { cfg.Timeout = time.Hour })
	ctx, cancel := context.WithCancel(ctxBG)
	time.AfterFunc(50*time.Millisecond, cancel)
	started := time.Now()
	_, err := c.ListRooms(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
	var e *Error
	if errors.As(err, &e) {
		t.Errorf("the caller's cancellation must not be an *Error: %+v", e)
	}
	within(t, started, 2*time.Second, "cancel mid-body")
	if srv.hits.Load() != 1 {
		t.Errorf("%d hits: a cancelled call is not retried", srv.hits.Load())
	}
}

// --- (e) redirects ---------------------------------------------------------------------

func TestAdversarial_RedirectLoopsAndOddLocations(t *testing.T) {
	var loop *loopback
	loop = serve(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		http.Redirect(w, r, loop.URL+r.URL.Path, http.StatusFound)
	})
	c := liveClient(t, "https://x.test", withRates(loop.URL), withRetries(0))
	started := time.Now()
	_, err := c.GetRates(ctxBG)
	// Go's client gives up after ten hops; that is a transport failure, reported
	// as this SDK's network error and never as a raw *url.Error.
	e := expectError(t, err, "network", 0, true, "")
	var uerr *url.Error
	if errors.As(err, &uerr) {
		t.Errorf("a raw *url.Error reached the caller: %v", err)
	}
	if !strings.HasPrefix(e.Message, "GET /api/v1/billing/rtc/rates/current: ") || !strings.Contains(e.Message, "redirects") {
		t.Errorf("%q", e.Message)
	}
	if hits := loop.hits.Load(); hits < 10 || hits > 12 {
		t.Errorf("%d hits, want Go's cap of ten hops", hits)
	}
	within(t, started, 5*time.Second, "redirect loop")

	for _, tc := range []struct {
		step   step
		status int
		to     string
	}{
		{step{status: 301, headers: map[string]string{"Location": "/elsewhere"}}, 301, " to /elsewhere"},
		{step{status: 302, headers: map[string]string{"Location": "ftp://files.test/x"}}, 302, " to ftp://files.test/x"},
		{step{status: 307, headers: map[string]string{"Location": "https://u:pw@evil.test/"}}, 307, " to https://[redacted]@evil.test/"},
		{step{status: 308, raw: Ptr(`{"error":"moved","message":"use https"}`), headers: map[string]string{"Location": "https://x.test/v1/rooms"}}, 308, " to https://x.test/v1/rooms"},
		{step{status: 304}, 304, ""},
	} {
		ft := scripted(tc.step)
		_, err := newClient(t, ft).ListRooms(ctxBG)
		expectError(t, err, "redirected", tc.status, false, fmt.Sprintf("GET /v1/rooms: the server answered %d%s; a request carrying the API key is never redirected. Point BaseURL at the controller itself (its exact scheme and host)", tc.status, tc.to))
		if ft.count() != 1 {
			t.Errorf("%d requests: a 3xx is neither followed nor retried", ft.count())
		}
	}
}

// --- (f) ids, origins and bodies -------------------------------------------------------

func TestAdversarial_OddIDsOriginsAndBodies(t *testing.T) {
	ft := scripted(jsonStep(200, map[string]any{}))
	c := newClient(t, ft)
	// A NUL and a byte that is not UTF-8 are bytes like any other: encoded, sent, no panic.
	if _, err := c.GetRoom(ctxBG, "a\x00b\xffc"); err != nil {
		t.Fatal(err)
	}
	if got := ft.call(0).Path; got != "/v1/rooms/a%00b%FFc" {
		t.Errorf("path %q", got)
	}
	// A BaseURL with a path prefix (a reverse proxy) keeps it; the slash is dropped once.
	c = newClient(t, ft, func(cfg *Config) { cfg.BaseURL = "https://x.test/rtc/" })
	_, _ = c.ListRooms(ctxBG)
	if got := ft.call(1).URL; got != "https://x.test/rtc/v1/rooms" {
		t.Errorf("URL %q", got)
	}
	// A query or a fragment in an origin can never reach the API: the path
	// would land in the query, or be dropped with the fragment. Refused at
	// startup, without repeating the value.
	for _, bad := range []string{"https://x.test?x=1", "https://x.test/#frag", "https://x.test/api?", "https://u:pw@x.test/#"} {
		_, err := NewStreamClient(Config{BaseURL: bad, APIKey: testKey})
		if err == nil || !strings.Contains(err.Error(), "BaseURL") || strings.Contains(err.Error(), "pw") || strings.Contains(err.Error(), "x.test") {
			t.Errorf("%q: %v", bad, err)
		}
		_, err = NewStreamClient(Config{BaseURL: "https://x.test", RatesBaseURL: bad, APIKey: testKey})
		if err == nil || !strings.Contains(err.Error(), "RatesBaseURL") {
			t.Errorf("rates %q: %v", bad, err)
		}
	}
	// A 1 MiB metadata is serialized once and resent byte for byte.
	ft = scripted(errorStep(503, "unavailable", "x"), jsonStep(200, map[string]any{}))
	big := strings.Repeat("m", 1<<20)
	if _, err := newClient(t, ft).EnsureRoom(ctxBG, EnsureRoomParams{RoomID: "r", HostIdentity: "h", Metadata: map[string]any{"blob": big}}); err != nil {
		t.Fatal(err)
	}
	if ft.count() != 2 || len(ft.call(0).Body) < 1<<20 || !bytes.Equal(ft.call(0).Body, ft.call(1).Body) {
		t.Errorf("%d attempts, bodies %d and %d bytes", ft.count(), len(ft.call(0).Body), len(ft.call(1).Body))
	}
	// Whatever encoding/json refuses fails before anything is sent, as a plain error.
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	for name, meta := range map[string]any{"NaN": math.NaN(), "a func": map[string]any{"f": func() {}}, "a cycle": cyclic, "a complex": complex(1, 2)} {
		ft = scripted(jsonStep(201, map[string]any{}))
		_, err := newClient(t, ft).SendMessage(ctxBG, SendMessageParams{Channel: "c", Metadata: meta})
		var e *Error
		if err == nil || errors.As(err, &e) || !strings.HasPrefix(err.Error(), "POST /v1/chat/c/messages: the body cannot be sent as JSON: ") || ft.count() != 0 {
			t.Errorf("%s: %v after %d requests", name, err, ft.count())
		}
	}
	// Zero-valued optional fields are left out. A field that was given as a nil
	// json.RawMessage or a typed nil pointer is sent as null: it was given.
	ft = scripted(jsonStep(200, map[string]any{}))
	c = newClient(t, ft)
	_, _ = c.CreateStreamToken(ctxBG, CreateStreamTokenParams{StreamID: "s", TTLSeconds: 0, Identity: ""})
	_, _ = c.SendMessage(ctxBG, SendMessageParams{Channel: "c", Text: "hi", Ephemeral: false})
	_, _ = c.EnsureRoom(ctxBG, EnsureRoomParams{RoomID: "r", HostIdentity: "h", Metadata: json.RawMessage(nil)})
	_, _ = c.EnsureRoom(ctxBG, EnsureRoomParams{RoomID: "r", HostIdentity: "h", Metadata: (*struct{ A int })(nil)})
	for i, want := range []string{`{}`, `{"text":"hi"}`, `{"hostIdentity":"h","metadata":null}`, `{"hostIdentity":"h","metadata":null}`} {
		if got := string(ft.call(i).Body); got != want {
			t.Errorf("body %d: %s, want %s", i, got, want)
		}
	}
	// A negative day count is sent as given; the server treats it as the default.
	_, _ = c.GetUsageSeries(ctxBG, -5)
	if got := ft.call(4).Path; got != "/v1/usage/series?days=-5" {
		t.Errorf("%q", got)
	}
}

// --- (g) replies of every wrong shape -----------------------------------------------------

func TestAdversarial_RepliesOfEveryWrongShape(t *testing.T) {
	for _, raw := range []string{`[]`, `"x"`, `123`, `true`, `{"room":"not an object"}`} {
		ft := scripted(rawStep(200, raw))
		_, err := newClient(t, ft).GetRoom(ctxBG, "r")
		e := expectError(t, err, "http-200", 200, false, "")
		if !strings.HasPrefix(e.Message, "GET /v1/rooms/r: the reply could not be decoded: ") || ft.count() != 1 {
			t.Errorf("%s: %q after %d attempts", raw, e.Message, ft.count())
		}
	}
	// null is the zero value, as Node hands back null.
	view, err := newClient(t, scripted(rawStep(200, `null`))).GetRoom(ctxBG, "r")
	if err != nil || view == nil || view.Room.RoomID != "" {
		t.Errorf("%v %v", view, err)
	}
	// A byte-order mark makes it not JSON, as JSON.parse says too.
	_, err = newClient(t, scripted(rawStep(200, "\xEF\xBB\xBF{}"))).ListRooms(ctxBG)
	e := expectError(t, err, "http-200", 200, false, "")
	if !strings.HasPrefix(e.Message, "GET /v1/rooms: the reply is not JSON: ") {
		t.Errorf("%q", e.Message)
	}
	// Invalid UTF-8 in the server's words: replaced, never a panic, the message stays valid UTF-8.
	_, err = newClient(t, scripted(rawStep(404, "{\"error\":\"no-room\",\"message\":\"r\xffm "+testKey+"\"}"))).GetRoom(ctxBG, "r")
	e = expectError(t, err, "no-room", 404, false, "")
	if !utf8.ValidString(e.Message) || strings.Contains(e.Message, testKey) {
		t.Errorf("%q", e.Message)
	}
	_, err = newClient(t, scripted(rawStep(502, "\xff\xfe<html>"+testKey+"</html>")), withRetries(0)).GetRoom(ctxBG, "r")
	e = expectError(t, err, "http-502", 502, true, "")
	if strings.Contains(e.Message, testKey) || !strings.Contains(e.Message, "[redacted]") {
		t.Errorf("%q", e.Message)
	}
	// Numbers no field can hold: the decode fails as http-200, nothing panics.
	for _, raw := range []string{`{"room":{"participants":1e100}}`, `{"room":{"createdAt":99999999999999999999}}`, `{"room":{"participants":-1.5}}`} {
		_, err := newClient(t, scripted(rawStep(200, raw))).GetRoom(ctxBG, "r")
		expectError(t, err, "http-200", 200, false, "")
	}
	// A 7 MiB reply, under the 8 MiB cap, is read whole (0.1.0 read a 10 MB
	// one here; the cap itself is pinned in replysize_test.go).
	big := `{"rooms":[{"roomId":"r","metadata":"` + strings.Repeat("x", 7<<20) + `"}]}`
	list, err := newClient(t, scripted(rawStep(200, big))).ListRooms(ctxBG)
	if err != nil || len(list.Rooms) != 1 || len(list.Rooms[0].Metadata) != 7<<20+2 {
		t.Errorf("%v", err)
	}
	// Nested error shapes.
	for _, tc := range []struct{ raw, code, message string }{
		{`{"error":{"code":5,"message":"m"}}`, "http-500", "GET /v1/rooms/r: m"},
		{`{"error":["x"]}`, "http-500", `GET /v1/rooms/r: {"error":["x"]}`},
		{`{"error":{"code":"X","message":"inner"},"message":"outer"}`, "X", "GET /v1/rooms/r: inner"},
		{`{"error":null,"message":"m"}`, "http-500", "GET /v1/rooms/r: m"},
		{`{"error":{},"message":"outer"}`, "http-500", `GET /v1/rooms/r: {"error":{},"message":"outer"}`},
		{`{"error":"  ","message":"m"}`, "  ", "GET /v1/rooms/r: m"},
	} {
		_, err := newClient(t, scripted(rawStep(500, tc.raw)), withRetries(0)).GetRoom(ctxBG, "r")
		expectError(t, err, tc.code, 500, true, tc.message)
	}
	// Metadata comes back exactly as sent, whitespace and key order included.
	view, err = newClient(t, scripted(rawStep(200, `{"room":{"metadata": { "b" : 1, "a" : [ ] }}}`))).GetRoom(ctxBG, "r")
	if err != nil || string(view.Room.Metadata) != `{ "b" : 1, "a" : [ ] }` {
		t.Errorf("%s %v", view.Room.Metadata, err)
	}
}

// --- (h) retries ----------------------------------------------------------------------------

func TestAdversarial_RetryAfterIgnoredHugeRetriesAndTheWaitCap(t *testing.T) {
	// Retry-After is not read (the Node SDK does not read it either): the
	// schedule is the SDK's own.
	limited := errorStep(429, "rate-limited", "slow")
	limited.headers = map[string]string{"Retry-After": "7"}
	ft := scripted(limited, jsonStep(200, map[string]any{}))
	c := newClient(t, ft)
	sleeps := pinClock(c)
	if _, err := c.GetBalance(ctxBG); err != nil || fmt.Sprint(*sleeps) != fmt.Sprint([]time.Duration{200 * time.Millisecond}) {
		t.Errorf("%v, waits %v", err, *sleeps)
	}
	// Retries at the top of int: the attempt count must not overflow.
	ft = scripted(errorStep(503, "unavailable", "x"), jsonStep(200, map[string]any{}))
	c = newClient(t, ft, withRetries(math.MaxInt))
	pinClock(c)
	if _, err := c.GetBalance(ctxBG); err != nil || ft.count() != 2 {
		t.Errorf("Retries MaxInt: %v after %d attempts", err, ft.count())
	}
	// The waits double and then hold at 2^31-1 ms, the same cap as the
	// deadline; they never overflow into a negative, instant wait.
	ft = scripted(errorStep(503, "unavailable", "x"))
	c = newClient(t, ft, withRetries(40))
	sleeps = pinClock(c)
	_, err := c.GetBalance(ctxBG)
	expectError(t, err, "unavailable", 503, true, "")
	if ft.count() != 41 || len(*sleeps) != 40 {
		t.Fatalf("%d attempts, %d waits", ft.count(), len(*sleeps))
	}
	for i, d := range *sleeps {
		if d <= 0 || d > maxTimeout || (i > 0 && d < (*sleeps)[i-1]) {
			t.Errorf("wait %d is %s", i+1, d)
		}
	}
	if (*sleeps)[0] != 200*time.Millisecond || (*sleeps)[3] != 1600*time.Millisecond || (*sleeps)[39] != maxTimeout {
		t.Errorf("waits %v", *sleeps)
	}
}

// --- (i) webhooks: grammar and clock edges ---------------------------------------------------

func TestAdversarial_WebhookHeaderAndClockEdges(t *testing.T) {
	body := []byte(goldenBody)
	secret := []string{"whsec_golden_current"}
	sig := "v1=" + goldenCurrent
	for header, want := range map[string]bool{
		"T=1787000000," + sig:                          false, // upper-case keys are other keys: no timestamp
		"t=1787000000,V1=" + goldenCurrent:             false, // no v1
		"t=1787000000,v1=" + goldenCurrent + "a":       false, // 65 hex characters
		"\tt=1787000000\t,\t" + sig + "\t":             true,  // tabs around a part are whitespace
		"t=1787000000 ," + sig:                         true,
		"t= 1787000000," + sig:                         false, // a space inside the value
		"t=+1787000000," + sig:                         false,
		"t=1787000000,v1 =" + goldenCurrent:            false, // "v1 " is another key
		"t=1787000000," + sig + ",v1=" + goldenCurrent: true,  // the same signature twice
		"t=1787000000,,":                               false,
	} {
		if got := VerifyWebhook(body, header, secret, lenient(goldenTS)); got != want {
			t.Errorf("%q: %v", header, got)
		}
	}
	// Exactly 4096 characters verifies; 4097 does not.
	pad := func(n int) string {
		h := "t=1787000000," + sig + ",v0="
		return h + strings.Repeat("a", n-len(h))
	}
	if !VerifyWebhook(body, pad(4096), secret, lenient(goldenTS)) || VerifyWebhook(body, pad(4097), secret, lenient(goldenTS)) {
		t.Error("the 4096-character bound is off by one")
	}
	// A header split across two header values: join them with "," as Node does.
	h := http.Header{}
	h.Add("X-Stream-Signature", "t=1787000000")
	h.Add("X-Stream-Signature", sig)
	if VerifyWebhook(body, h.Get("X-Stream-Signature"), secret, lenient(goldenTS)) {
		t.Error("the first value alone has no signature")
	}
	if !VerifyWebhook(body, strings.Join(h.Values("X-Stream-Signature"), ","), secret, lenient(goldenTS)) {
		t.Error("the values joined with a comma must verify")
	}
	// nil and empty bodies: the signature covers "<t>." and nothing else.
	empty := "t=1787000000,v1=" + hmacHex("whsec_golden_current", "1787000000.")
	if !VerifyWebhook(nil, empty, secret, at(goldenTS)) || !VerifyWebhook([]byte{}, empty, secret, at(goldenTS)) || VerifyWebhook([]byte("x"), empty, secret, at(goldenTS)) {
		t.Error("an empty body")
	}
	// A secret with whitespace around it is another secret: nothing is trimmed, as in Node.
	if VerifyWebhook(body, "t=1787000000,"+sig, []string{" whsec_golden_current "}, at(goldenTS)) {
		t.Error("a padded secret verified")
	}
	// A timestamp 31 million years ahead, signed with the real secret: Node
	// refuses it (|now - t| is far above any tolerance), and so must this SDK.
	// time.Duration saturates at about 292 years, and the negation of its
	// minimum is itself, so a naive abs(now.Sub(t)) comes out negative.
	far := "999999999999999"
	if VerifyWebhook(body, "t="+far+",v1="+hmacHex("whsec_golden_current", far+"."+goldenBody), secret, at(goldenTS)) {
		t.Error("a timestamp 31 million years in the future passed the freshness check")
	}
	// A timestamp 584 years ahead, chosen so that (now - t) in nanoseconds
	// wraps around int64 to 0.29 s: only the range guard refuses it.
	wrap := "20233744074"
	if VerifyWebhook(body, "t="+wrap+",v1="+hmacHex("whsec_golden_current", wrap+"."+goldenBody), secret, at(goldenTS)) {
		t.Error("a timestamp chosen to wrap the nanosecond arithmetic passed the freshness check")
	}
	zero := &VerifyOptions{Now: func() time.Time { return time.Time{} }}
	if VerifyWebhook(body, "t=1787000000,"+sig, secret, zero) {
		t.Error("a clock 2000 years off passed the freshness check")
	}
	zero.Tolerance = Ptr(time.Duration(math.MaxInt64))
	if VerifyWebhook(body, "t=1787000000,"+sig, secret, zero) {
		t.Error("2000 years is beyond any tolerance a Duration can hold")
	}
	huge := at(2102360000)
	huge.Tolerance = Ptr(time.Duration(math.MaxInt64))
	if !VerifyWebhook(body, "t=1787000000,"+sig, secret, huge) {
		t.Error("the largest tolerance must accept a delivery years old")
	}
	// The signed prefix is the header's timestamp text, as Node signs and
	// verifies it: a timestamp written with leading zeros is signed with them.
	padded := "0001787000000"
	if !VerifyWebhook(body, "t="+padded+",v1="+hmacHex("whsec_golden_current", padded+"."+goldenBody), secret, at(goldenTS)) {
		t.Error("a signature over the timestamp as written did not verify")
	}
	if VerifyWebhook(body, "t="+padded+","+sig, secret, at(goldenTS)) {
		t.Error("a signature over the canonical number verified a header that spells it differently")
	}
	// ParseWebhook: unknown fields are ignored, data of any shape is kept as sent.
	odd := `{"id":"e","type":"x.y","tenantId":"t","createdAt":1,"data":[1, 2],"future":{"a":1}}`
	ev, err := ParseWebhook([]byte(odd), "t=1787000000,v1="+hmacHex("s", "1787000000."+odd), []string{"s"}, at(goldenTS))
	if err != nil || ev.ID != "e" || string(ev.Data) != `[1, 2]` {
		t.Errorf("%+v %v", ev, err)
	}
}

// --- (j) concurrency --------------------------------------------------------------------------

func TestAdversarial_ThirtyTwoGoroutinesShareOneClientAndTheRatesHost(t *testing.T) {
	ctrl := serve(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		switch r.URL.Path {
		case "/v1/rooms":
			_, _ = w.Write([]byte(`{"rooms":[{"roomId":"r"}]}`))
		case "/v1/billing/balance":
			w.WriteHeader(401)
			_, _ = fmt.Fprintf(w, `{"error":"unauthorized","message":"bad key %s"}`, testKey)
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no-room","message":"not found"}`))
		}
	})
	rates := serve(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		if r.Header.Get("X-API-Key") != "" {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"version":9}}`))
	})
	c := liveClient(t, ctrl.URL, withRates(rates.URL))
	var wg sync.WaitGroup
	problems := make(chan string, 32*8*4)
	for g := 0; g < 32; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 8; i++ {
				list, err := c.ListRooms(ctxBG)
				if err != nil || len(list.Rooms) != 1 {
					problems <- fmt.Sprintf("goroutine %d: ListRooms %v", g, err)
				}
				_, err = c.GetBalance(ctxBG)
				var e *Error
				if !errors.As(err, &e) || e.Code != "unauthorized" || e.Message != "GET /v1/billing/balance: bad key [redacted]" {
					problems <- fmt.Sprintf("goroutine %d: GetBalance %v", g, err)
				}
				r, err := c.GetRates(ctxBG)
				if err != nil || r.Version != 9 {
					problems <- fmt.Sprintf("goroutine %d: GetRates %v", g, err)
				}
				if s := fmt.Sprint(c); strings.Contains(s, testKey) {
					problems <- "the printed client shows the key"
				}
				if j, _ := json.Marshal(c); strings.Contains(string(j), testKey) {
					problems <- "the marshalled client shows the key"
				}
			}
		}(g)
	}
	wg.Wait()
	close(problems)
	for p := range problems {
		t.Error(p)
	}
	if ctrl.hits.Load() != 32*8*2 || rates.hits.Load() != 32*8 {
		t.Errorf("hits: controller %d, rates %d", ctrl.hits.Load(), rates.hits.Load())
	}
}

// --- (b) secrets on every printing surface Go has ---------------------------------------------

func TestAdversarial_EveryPrintingSurfaceHidesTheSecrets(t *testing.T) {
	key := "sk_live_Zm9v+YmFyLzk/QmF6+cXV4/dGVzdF9rZXk+MTIz/NDU2Nzg=="
	cfg := Config{
		BaseURL: "https://svc:hunter2@x.test/", APIKey: key, RatesBaseURL: "https://web:s3cret@api.example.com",
		Timeout: time.Second, Retries: Ptr(1), HTTPClient: &http.Client{Transport: scripted()},
	}
	c, err := NewStreamClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Without an HTTPClient too: with one, encoding/json refuses the whole
	// struct (CheckRedirect is a func) and nothing leaks by accident.
	plain := Config{BaseURL: "https://svc:hunter2@x.test/", APIKey: key, RatesBaseURL: "https://web:s3cret@api.example.com"}
	e := &Error{Code: "unauthorized", Status: 401, Message: "GET /v1/rooms: bad key [redacted]"}
	type app struct {
		Cfg    Config
		Ptr    *Config
		Client *StreamClient
		Err    *Error
	}
	a := app{plain, &cfg, c, e}
	var views []string
	// Every fmt verb, on the value and the pointer, alone and nested in a
	// struct. %p is for pointers: on a value, fmt reports the bad verb by
	// printing the raw struct, and go vet flags the call; it is checked on
	// the pointers below.
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%t", "%08.3f", "%c", "%U", "%T"} {
		for _, v := range []any{cfg, &cfg, plain, &plain, c, *c, a, &a, e} {
			views = append(views, verb+": "+fmt.Sprintf(verb, v))
		}
	}
	views = append(views, fmt.Sprintf("%p %p %p", &cfg, c, &a), fmt.Sprint(cfg, plain, c, a), fmt.Sprintln(cfg, plain, c, a))
	// Through an unexported field fmt cannot call methods, and a bad verb makes
	// it print the pointed-to struct with %v: the secrets must then be
	// unreadable in themselves, which is why they are closures.
	type hidden struct{ client *StreamClient }
	for _, verb := range []string{"%v", "%+v", "%#v", "%t", "%c", "%d", "%f"} {
		views = append(views, verb+" hidden: "+fmt.Sprintf(verb, hidden{c}), verb+" secrets: "+fmt.Sprintf(verb, c.s), verb+" secrets value: "+fmt.Sprintf(verb, *c.s))
	}
	// encoding/json, direct and nested: structured loggers (zap, zerolog, logrus) go through it.
	for _, v := range []any{cfg, &cfg, plain, &plain, c, *c, a, map[string]any{"cfg": plain, "client": c}} {
		j, err := json.Marshal(v)
		if err != nil {
			t.Errorf("json.Marshal(%T): %v", v, err)
		}
		views = append(views, "json: "+string(j))
	}
	// log/slog with both handlers.
	for _, handler := range []func(io.Writer) slog.Handler{
		func(w io.Writer) slog.Handler { return slog.NewTextHandler(w, nil) },
		func(w io.Writer) slog.Handler { return slog.NewJSONHandler(w, nil) },
	} {
		var buf bytes.Buffer
		slog.New(handler(&buf)).Info("boot", "cfg", cfg, "cfgp", &cfg, "plain", plain, "client", c, "app", a, slog.Any("err", e))
		views = append(views, "slog: "+buf.String())
	}
	views = append(views, errors.Join(e, fmt.Errorf("wrapped: %w", e)).Error())
	for _, v := range views {
		for _, secret := range []string{key, "hunter2", "s3cret", "Zm9v"} {
			if strings.Contains(v, secret) {
				t.Errorf("%s shows %q", v, secret)
			}
		}
	}
	// The shape, not only the absence of the key: every verb gives the one
	// redacted view (Format), and slog gets a group (LogValue), so a line
	// reads cfg.apiKey=[redacted] rather than a quoted struct.
	for _, verb := range []string{"%t", "%c", "%d", "%08.3f"} {
		if got := fmt.Sprintf(verb, c); got != c.String() {
			t.Errorf("%s of the client: %q", verb, got)
		}
		if got := fmt.Sprintf(verb, plain); got != plain.String() {
			t.Errorf("%s of a Config: %q", verb, got)
		}
	}
	var text bytes.Buffer
	slog.New(slog.NewTextHandler(&text, nil)).Info("boot", "cfg", plain, "client", c)
	for _, want := range []string{"cfg.apiKey=[redacted]", "cfg.baseUrl=https://[redacted]@x.test/", "client.baseUrl=https://[redacted]@x.test"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("slog text %q lacks %q", text.String(), want)
		}
	}
	if j, _ := json.Marshal(plain); string(j) != `{"baseUrl":"https://[redacted]@x.test/","apiKey":"[redacted]","ratesBaseUrl":"https://[redacted]@api.example.com","timeoutMs":0,"retries":null}` {
		t.Errorf("json of a Config: %s", j)
	}
}
