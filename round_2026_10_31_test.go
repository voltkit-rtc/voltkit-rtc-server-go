package voltkit

// The testers' report of 2026-10-31 (G012-1 to G012-5, X-1, X-2) and the lead's
// decisions D4 to D9 of that round. Each test here failed against 0.1.2 before
// its fix, except the G012-5 one, which pins documented behaviour (see there).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// --- G012-1, X-1 (D4): a 2xx that is not the shape the call needs ------------------

func TestG012_1_AReplyWithoutWhatTheCallNeedsIsRefused(t *testing.T) {
	cases := []struct {
		name    string
		call    func(c *StreamClient) (any, error)
		reply   step
		message string
	}{
		{"CreateToken answered {}", func(c *StreamClient) (any, error) {
			return c.CreateToken(ctxBG, CreateTokenParams{RoomID: "r", Identity: "u", Role: RoleViewer})
		}, jsonStep(200, map[string]any{}), `POST /v1/rooms/r/tokens: the reply is not the expected shape: "token" is not a non-empty string`},
		{"CreateToken answered an empty body", func(c *StreamClient) (any, error) {
			return c.CreateToken(ctxBG, CreateTokenParams{RoomID: "r", Identity: "u", Role: RoleViewer})
		}, rawStep(200, ""), `POST /v1/rooms/r/tokens: the reply is not the expected shape: it is not an object`},
		{"EnsureRoom answered {}: no existed is added", func(c *StreamClient) (any, error) {
			return c.EnsureRoom(ctxBG, EnsureRoomParams{RoomID: "r", HostIdentity: "h"})
		}, jsonStep(200, map[string]any{}), `PUT /v1/rooms/r: the reply is not the expected shape: "roomId" is not a non-empty string`},
		{"CreateRoom answered a list", func(c *StreamClient) (any, error) {
			return c.CreateRoom(ctxBG, CreateRoomParams{HostIdentity: "h"})
		}, rawStep(201, `[{"roomId":"r"}]`), `POST /v1/rooms: the reply is not the expected shape: it is not an object`},
		{"ListParticipants answered a null list", func(c *StreamClient) (any, error) {
			return c.ListParticipants(ctxBG, "r")
		}, rawStep(200, `{"participants":null}`), `GET /v1/rooms/r/participants: the reply is not the expected shape: "participants" is not an array`},
		{"GetUsage answered null", func(c *StreamClient) (any, error) {
			return c.GetUsage(ctxBG)
		}, rawStep(200, `null`), `GET /v1/usage: the reply is not the expected shape: it is not an object`},
	}
	for _, tc := range cases {
		ft := scripted(tc.reply)
		got, err := tc.call(newClient(t, ft, withRetries(2)))
		if err == nil {
			t.Errorf("%s: resolved to %+v, want an http-200 error", tc.name, got)
			continue
		}
		var e *Error
		if !errors.As(err, &e) {
			t.Errorf("%s: %T %v, want an *Error", tc.name, err, err)
			continue
		}
		if e.Code != fmt.Sprintf("http-%d", tc.reply.status) || e.Status != tc.reply.status || e.Retryable || e.Message != tc.message {
			t.Errorf("%s: {%q %d %v %q}, want {http-%d %d false %q}", tc.name, e.Code, e.Status, e.Retryable, e.Message, tc.reply.status, tc.reply.status, tc.message)
		}
		if ft.count() != 1 {
			t.Errorf("%s: %d attempts, want 1", tc.name, ft.count())
		}
	}
}

// A decoder that matches keys case-insensitively (encoding/json) must not let a
// second spelling of the required field replace the one the check read: the
// check reads the exact key, and the model holds what the check passed.
func TestG012_1_TheCheckedFieldIsTheOneReturned(t *testing.T) {
	ft := scripted(rawStep(200, `{"token":"t-1","Token":"","TOKEN":""}`))
	tok, err := newClient(t, ft).CreateToken(ctxBG, CreateTokenParams{RoomID: "r", Identity: "u", Role: RoleViewer})
	if err != nil || tok.Token != "t-1" {
		t.Errorf("CreateToken: %+v, %v; want the token t-1", tok, err)
	}
	ft = scripted(rawStep(200, `{"roomId":"r-1","roomID":""}`))
	room, err := newClient(t, ft).EnsureRoom(ctxBG, EnsureRoomParams{RoomID: "r", HostIdentity: "h"})
	if err != nil || room.RoomID != "r-1" || !room.Existed {
		t.Errorf("EnsureRoom: %+v, %v; want r-1, existed", room, err)
	}
}

// --- G012-2 (D9's sibling): the key escaped for HTML, then by Go's JSON encoder ----

func TestG012_2_TheKeyAsGoJSONEscapedHTMLIsRedacted(t *testing.T) {
	rest := strings.TrimPrefix(testKey, "sk_")
	for _, form := range []string{`sk\u0026lowbar;`, `sk\u0026#95;`, `sk\u0026#x5f;`, `sk\u0026#x005F;`, `sk\u0026#0095;`} {
		echo := "bad key " + form + rest
		errorBody, _ := json.Marshal(map[string]string{"error": "unauthorized", "message": echo})
		for _, st := range []step{rawStep(401, string(errorBody)), rawStep(502, echo), rawStep(200, echo)} {
			_, err := newClient(t, scripted(st), withRetries(0)).ListRooms(ctxBG)
			e := asError(t, err)
			j, _ := json.Marshal(e)
			for _, view := range []string{e.Message, err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), string(j)} {
				if strings.Contains(view, rest[:12]) || !strings.Contains(view, "[redacted]") {
					t.Errorf("%s (status %d): %s", form, st.status, view)
				}
			}
		}
	}
}

// --- G012-3, X-2 (D5): whole seconds -------------------------------------------------

func TestG012_3_ToleranceZeroAcceptsTheWholeCurrentSecond(t *testing.T) {
	const secret = "whsec_round_2026_10_31"
	const ts = 1787000000
	body := []byte(`{"id":"evt_1","type":"webhook.test","tenantId":"t","createdAt":1787000000000}`)
	header := fmt.Sprintf("t=%d,v1=%s", ts, hmacHex(secret, fmt.Sprintf("%d.%s", ts, body)))
	verify := func(now time.Time, tolerance *time.Duration) bool {
		return VerifyWebhook(body, header, []string{secret}, &VerifyOptions{Tolerance: tolerance, Now: func() time.Time { return now }})
	}
	zero := Ptr(time.Duration(0))
	accepted := 0
	for k := 0; k < 200; k++ {
		if verify(time.Unix(ts, int64(k)*int64(5*time.Millisecond)), zero) {
			accepted++
		}
	}
	if accepted != 200 {
		t.Errorf("tolerance 0: %d of 200 deliveries within their own second verified, want 200", accepted)
	}
	for _, tc := range []struct {
		name string
		now  time.Time
		tol  *time.Duration
		want bool
	}{
		{"tolerance 0, the next second", time.Unix(ts+1, 0), zero, false},
		{"tolerance 0, the last nanosecond before the second", time.Unix(ts, -1), zero, false},
		{"default, 300.999 s old", time.Unix(ts+300, 999_999_999), nil, true},
		{"default, 301 s old", time.Unix(ts+301, 0), nil, false},
		{"default, 300 s ahead", time.Unix(ts-300, 0), nil, true},
		{"default, the clock 300.5 s before", time.Unix(ts-301, 500_000_000), nil, false},
		{"tolerance 1.5 s, 2.9 s old: 2 whole seconds", time.Unix(ts+2, 900_000_000), Ptr(1500 * time.Millisecond), false},
		{"tolerance 1.5 s, 1.9 s old: 1 whole second", time.Unix(ts+1, 900_000_000), Ptr(1500 * time.Millisecond), true},
	} {
		if got := verify(tc.now, tc.tol); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// --- G012-4: metadata that would be sent as null ------------------------------------

func TestG012_4_MetadataThatEncodesAsNullIsLeftOut(t *testing.T) {
	var nilMap map[string]any
	var nilPointer *struct{ A int }
	nulls := []struct {
		name string
		meta any
	}{
		{"a nil map", nilMap}, {"a nil slice", []string(nil)}, {"a nil pointer", nilPointer},
		{"a nil json.RawMessage", json.RawMessage(nil)}, {"json.RawMessage null", json.RawMessage(" null ")},
		{"a pointer to a nil map", &nilMap},
	}
	calls := []struct {
		name  string
		reply step
		do    func(c *StreamClient, meta any) error
	}{
		{"CreateRoom", jsonStep(201, map[string]any{"roomId": "r"}), func(c *StreamClient, meta any) error {
			_, err := c.CreateRoom(ctxBG, CreateRoomParams{HostIdentity: "h", Metadata: meta})
			return err
		}},
		{"EnsureRoom", jsonStep(201, map[string]any{"roomId": "r"}), func(c *StreamClient, meta any) error {
			_, err := c.EnsureRoom(ctxBG, EnsureRoomParams{RoomID: "r", HostIdentity: "h", Metadata: meta})
			return err
		}},
		{"SendMessage", jsonStep(201, map[string]any{"id": "1-0"}), func(c *StreamClient, meta any) error {
			_, err := c.SendMessage(ctxBG, SendMessageParams{Channel: "c", Metadata: meta})
			return err
		}},
		{"CreateStream", jsonStep(201, map[string]any{"streamId": "s"}), func(c *StreamClient, meta any) error {
			_, err := c.CreateStream(ctxBG, CreateStreamParams{StreamID: "s", Metadata: meta})
			return err
		}},
		{"UpdateStream", jsonStep(200, map[string]any{"streamId": "s"}), func(c *StreamClient, meta any) error {
			_, err := c.UpdateStream(ctxBG, "s", UpdateStreamParams{Metadata: meta})
			return err
		}},
	}
	for _, call := range calls {
		for _, n := range nulls {
			ft := scripted(call.reply)
			if err := call.do(newClient(t, ft), n.meta); err != nil {
				t.Errorf("%s with %s: %v", call.name, n.name, err)
				continue
			}
			var sent map[string]json.RawMessage
			if err := json.Unmarshal(ft.call(0).Body, &sent); err != nil {
				t.Errorf("%s with %s: body %s", call.name, n.name, ft.call(0).Body)
				continue
			}
			if raw, ok := sent["metadata"]; ok {
				t.Errorf("%s with %s: sent \"metadata\":%s, want it left out", call.name, n.name, raw)
			}
		}
		// A value is still sent, whatever it is, an empty one included.
		for _, meta := range []any{map[string]any{}, []int{}, json.RawMessage(`{"a":1}`), 0, false, ""} {
			ft := scripted(call.reply)
			if err := call.do(newClient(t, ft), meta); err != nil {
				t.Errorf("%s with %#v: %v", call.name, meta, err)
				continue
			}
			var sent map[string]json.RawMessage
			_ = json.Unmarshal(ft.call(0).Body, &sent)
			want, _ := json.Marshal(meta)
			if string(sent["metadata"]) != string(want) {
				t.Errorf("%s with %#v: sent %s, want \"metadata\":%s", call.name, meta, ft.call(0).Body, want)
			}
		}
	}
	// The caller's params are not changed.
	params := SendMessageParams{Channel: "c", Text: "hi", Metadata: nilMap}
	_, _ = newClient(t, scripted(jsonStep(201, map[string]any{"id": "1-0"}))).SendMessage(ctxBG, params)
	if m, ok := params.Metadata.(map[string]any); !ok || m != nil {
		t.Errorf("the caller's Metadata became %#v", params.Metadata)
	}
}

// --- G012-5: a Config in an unexported field -------------------------------------

// fmt cannot call a method of a value it reached through an unexported field
// (reflect.Value.CanInterface is false), so Config's String and Format are not
// consulted there and its APIKey, a string, prints as it is. Giving the key a
// type of its own would not change that unless the type held no string at all
// (a closure, as StreamClient does), and that would break every
// Config{APIKey: os.Getenv(...)}. So 0.1.3 documents it (Config's doc comment
// and the README's "The client never shows its key"): keep the *StreamClient,
// not the Config. This test pins both halves; if fmt ever stops printing the
// field, it fails, and the documentation should say so.
func TestG012_5_AConfigInAnUnexportedFieldPrintsItsKeyAndAClientDoesNot(t *testing.T) {
	type holdsConfig struct{ cfg Config }
	if shown := fmt.Sprintf("%+v", holdsConfig{Config{BaseURL: "https://x.test", APIKey: testKey}}); !strings.Contains(shown, testKey) {
		t.Errorf("fmt no longer prints a Config in an unexported field (%s): update Config's doc comment and the README", shown)
	}
	c := newClient(t, scripted(jsonStep(200, map[string]any{"rooms": []any{}})), func(cfg *Config) {
		cfg.BaseURL = "https://svc:hunter2@x.test"
	})
	type holdsClient struct {
		client *StreamClient
		value  StreamClient
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%d", "%t", "%x", "%q"} {
		if shown := fmt.Sprintf(verb, holdsClient{c, *c}); strings.Contains(shown, testKey) || strings.Contains(shown, "hunter2") {
			t.Errorf("%s of a StreamClient in an unexported field: %s", verb, shown)
		}
	}
}

// --- D5: a whitespace-only webhook secret is no secret ----------------------------

func TestD5_AWhitespaceOnlySecretCountsAsAbsent(t *testing.T) {
	const ts = 1787000000
	const real = "whsec_round_2026_10_31"
	body := []byte(`{"id":"evt_1","type":"webhook.test","tenantId":"t","createdAt":1787000000000}`)
	signedWith := func(secret string) string {
		return fmt.Sprintf("t=%d,v1=%s", ts, hmacHex(secret, fmt.Sprintf("%d.%s", ts, body)))
	}
	opts := &VerifyOptions{Now: func() time.Time { return time.Unix(ts, 0) }}
	for _, blank := range []string{" ", "\t", "\n", "\r", "\v", "\f", " \t\r\n ", "\u00a0", "\u0085", "\ufeff", "\u2028", "\u3000"} {
		_, err := ParseWebhook(body, signedWith(blank), []string{blank}, opts)
		var e *Error
		if err == nil || errors.As(err, &e) || !strings.Contains(err.Error(), "needs the signing secret") {
			t.Errorf("%q alone: %v, want the missing-secret error", blank, err)
		}
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%q alone: VerifyWebhook did not panic", blank)
				}
			}()
			VerifyWebhook(body, signedWith(blank), []string{blank, ""}, opts)
		}()
		if VerifyWebhook(body, signedWith(blank), []string{blank, real}, opts) || VerifyWebhook(body, signedWith(blank), []string{real, blank}, opts) {
			t.Errorf("a delivery signed with %q verified beside a real secret", blank)
		}
		if !VerifyWebhook(body, signedWith(real), []string{blank, real}, opts) {
			t.Errorf("the real secret no longer verifies beside %q", blank)
		}
	}
	// Whitespace around real text is part of the secret, as it always was.
	if !VerifyWebhook(body, signedWith(" "+real+"\n"), []string{" " + real + "\n"}, opts) || VerifyWebhook(body, signedWith(real), []string{" " + real + "\n"}, opts) {
		t.Errorf("a secret with whitespace around it must stay the whole string")
	}
}

// --- D7: a URL password may hold @ / ? # ------------------------------------------

func TestD7_URLCredentialsWithAPasswordHoldingAtSlashQuestionHash(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://user:p@ssword@h", "https://[redacted]@h"},
		{"https://user:hunter2/x@h", "https://[redacted]@h"},
		{"https://user:pa?ss@h", "https://[redacted]@h"},
		{"https://user:pa#ss@h", "https://[redacted]@h"},
		{"https://:only/pw@h", "https://[redacted]@h"},
		{"https://user:12a/x@h", "https://[redacted]@h"},
		{"HTTPS://USER:P/W@H", "HTTPS://[redacted]@H"},
		{"see https://user:pa/ss@h and https://u:p#q@i.", "see https://[redacted]@h and https://[redacted]@i."},
		{"a://b@c://d:e/f@g", "a://[redacted]@c://[redacted]@g"},
		{"https://user:pw@x.test:9/v1@x", "https://[redacted]@x.test:9/v1@x"},
		// Left alone: no password, or one that reads as a port.
		{"https://h/a@b", "https://h/a@b"},
		{"https://h:8080/a@b", "https://h:8080/a@b"},
		{"https://h?e=a@b", "https://h?e=a@b"},
		{"https://h#a@b", "https://h#a@b"},
		{"mailto:me@host", "mailto:me@host"},
		{"https://user:123/x@h", "https://user:123/x@h"},
		{"1+://u:p@h", "1+://u:p@h"},
		{"no url here @ all", "no url here @ all"},
	} {
		if got := redactURLCredentials(tc.in); got != tc.want {
			t.Errorf("redactURLCredentials(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// referenceURLCredentials is the D7 pattern run the way a backtracking regex
// engine runs (?i)([a-z][a-z0-9+.-]*://)(?:[^\s/?#]*@|[^\s/?#:]*:(?!\d*(?:[/?#\s]|$))\S*@)
// with a global replace over the whole text: from every position, quadratic,
// and obviously that pattern. The fast redactURLCredentials must agree with it.
func referenceURLCredentials(text string) string {
	isSpace := func(i int) bool { r, _ := utf8.DecodeRuneInString(text[i:]); return isJSWhitespace(r) }
	runeEnd := func(i int) int { _, n := utf8.DecodeRuneInString(text[i:]); return i + n }
	// lastAt is the last @ in text[from:] before the first rune stop says to
	// stop at, or -1.
	lastAt := func(from int, stop func(i int) bool) int {
		at := -1
		for i := from; i < len(text) && !stop(i); i = runeEnd(i) {
			if text[i] == '@' {
				at = i
			}
		}
		return at
	}
	matchAt := func(s int) (schemeEnd, end int, ok bool) {
		if !isASCIILetter(text[s]) {
			return 0, 0, false
		}
		// The lookbehind (?<![a-z][0-9+.-]*), as Node writes it.
		k := s
		for k > 0 && strings.IndexByte("0123456789+.-", text[k-1]) >= 0 {
			k--
		}
		if k > 0 && isASCIILetter(text[k-1]) {
			return 0, 0, false
		}
		i := s + 1
		for i < len(text) && isSchemeByte(text[i]) {
			i++
		}
		if !strings.HasPrefix(text[i:], "://") {
			return 0, 0, false
		}
		u := i + 3
		if at := lastAt(u, func(j int) bool { return isSpace(j) || strings.IndexByte("/?#", text[j]) >= 0 }); at >= 0 {
			return u, at + 1, true
		}
		j := u
		for j < len(text) && !isSpace(j) && strings.IndexByte("/?#:", text[j]) < 0 {
			j = runeEnd(j)
		}
		if j >= len(text) || text[j] != ':' {
			return 0, 0, false
		}
		d := j + 1
		for d < len(text) && '0' <= text[d] && text[d] <= '9' {
			d++
		}
		if d == len(text) || isSpace(d) || strings.IndexByte("/?#", text[d]) >= 0 {
			return 0, 0, false
		}
		if at := lastAt(j+1, isSpace); at >= 0 {
			return u, at + 1, true
		}
		return 0, 0, false
	}
	var out strings.Builder
	last := 0
	for s := 0; s < len(text); {
		if schemeEnd, end, ok := matchAt(s); ok {
			out.WriteString(text[last:schemeEnd])
			out.WriteString("[redacted]@")
			last, s = end, end
			continue
		}
		s = runeEnd(s)
	}
	out.WriteString(text[last:])
	return out.String()
}

// isJSWhitespace is JavaScript's \s, which the Node SDK's pattern uses: its
// WhiteSpace and LineTerminator. Not Go's unicode.IsSpace, which adds U+0085
// and lacks U+FEFF.
func isJSWhitespace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return 0x2000 <= r && r <= 0x200A
}

func TestD7_URLCredentialsAgreeWithThePattern(t *testing.T) {
	pieces := []string{"a", "B", "z", "1", "0", "8", "+", ".", "-", ":", "/", "//", "@", "?", "#", " ", "\t", "\n",
		"://", "https://", "a://", "user:", ":80", "é", "\u00a0", "\u0085", "\ufeff", "\u2028", "\u3000", "\xff"}
	r := rand.New(rand.NewPCG(20261031, 7))
	for n := 0; n < 100_000; n++ {
		var b strings.Builder
		for k := r.IntN(14); k >= 0; k-- {
			b.WriteString(pieces[r.IntN(len(pieces))])
		}
		in := b.String()
		if got, want := redactURLCredentials(in), referenceURLCredentials(in); got != want {
			t.Fatalf("redactURLCredentials(%q)\n got %q\nwant %q (the pattern)", in, got, want)
		}
	}
}

func FuzzURLCredentials(f *testing.F) {
	for _, seed := range []string{"https://user:p@ssword@h", "https://user:hunter2/x@h", "a://b:c@", "https://h:8080/a@b", "x://y://z@w"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		if got, want := redactURLCredentials(in), referenceURLCredentials(in); got != want {
			t.Fatalf("redactURLCredentials(%q)\n got %q\nwant %q (the pattern)", in, got, want)
		}
	})
}

// Run over a whole page, \S*@ scans to the end of the word and back from every
// name: after a ://; a large page took minutes in Node (spec 5.5). Every page
// here is about 300 KB; each must take well under a second.
func TestD7_ALongPageStaysFast(t *testing.T) {
	const size = 300_000
	page := func(unit, tail string) string { return strings.Repeat(unit, size/len(unit)) + tail }
	for _, text := range []string{
		page("a://b:c", ""), page("a://b:c", "@"), page("a://b:1/", "@"), page("a://", "@"), page("a://b", "@"),
		page("a://:", "@"), page("https://u:p@", ""), page("a", "://b:c@"), page("1+", "a://b:c@"), page(":", "a://@"),
		page("a://b:c d", "@"), page("a@", "://b:c"),
	} {
		start := time.Now()
		_ = redactURLCredentials(text)
		if took := time.Since(start); took > time.Second {
			t.Errorf("a page of %q: %s", text[:12], took)
		}
	}
	// The key's zero-padded references (N049-5) are linear too.
	r := newKeyRedactor(testKey)
	for _, text := range []string{page("&#", "0"), "&#" + strings.Repeat("0", size), page("&#x0000000", ""), page("&amp;#000", "")} {
		start := time.Now()
		_ = r.redact(text)
		if took := time.Since(start); took > time.Second {
			t.Errorf("a page of %q: %s", text[:8], took)
		}
	}
}

// --- D8: a bad BaseURL fails when the client is made ------------------------------

// User info in BaseURL stays accepted because Go's transport sends it: net/http
// turns it into Authorization: Basic, beside X-API-Key. Proved here against a
// real loopback server, for each URL the vectors name.
func TestD8_UserInfoInBaseURLGoesOutWithTheRealTransport(t *testing.T) {
	var mu sync.Mutex
	var seen []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rooms":[]}`))
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	for i, tc := range []struct{ userinfo, basic string }{
		{"user:hunter2", "user:hunter2"},
		{"hunter2", "hunter2:"},
		{"user:p@hunter2", "user:p@hunter2"},
	} {
		c, err := NewStreamClient(Config{BaseURL: "http://" + tc.userinfo + "@" + host + "/", APIKey: testKey})
		if err != nil {
			t.Errorf("%s: refused: %v", tc.userinfo, err)
			continue
		}
		if _, err := c.ListRooms(context.Background()); err != nil {
			t.Errorf("%s: %v", tc.userinfo, err)
			continue
		}
		mu.Lock()
		h := seen[len(seen)-1]
		mu.Unlock()
		if got, want := h.Get("Authorization"), "Basic "+base64.StdEncoding.EncodeToString([]byte(tc.basic)); got != want {
			t.Errorf("%s: Authorization %q, want %q", tc.userinfo, got, want)
		}
		if h.Get("X-API-Key") != testKey {
			t.Errorf("%s: X-API-Key %q", tc.userinfo, h.Get("X-API-Key"))
		}
		if shown := fmt.Sprintf("%v", c); strings.Contains(shown, "hunter2") || !strings.Contains(shown, "[redacted]@"+host) {
			t.Errorf("%s: printed as %s", tc.userinfo, shown)
		}
		if len(seen) != i+1 {
			t.Errorf("%d requests, want %d", len(seen), i+1)
		}
	}
}

// --- D9: no error holds the key where an error reporter reads it --------------------

// Go has no frame locals for a reporter to read (Sentry, Bugsnag and Rollbar
// record an error's type, its Error() and a stack of program counters), and a
// crash's traceback prints arguments as machine words, never a string's bytes.
// What a reporter does read is the error value itself: Error(), %+v, its
// fields by reflection and every error it wraps. Each failure the transport
// can produce is walked here, unexported fields included, for the key, the
// password of BaseURL and the reply's unredacted text.
func TestD9_NoErrorValueHoldsTheKeyOrTheReplyText(t *testing.T) {
	const pw = "hunter2pw"
	echo := "key " + testKey + " and more words"
	cases := []struct {
		name string
		st   step
	}{
		{"a transport error quoting the key and the URL", step{network: true, errText: `Get "https://svc:` + pw + `@x.test/v1/rooms": X-API-Key ` + testKey}},
		{"an error reply echoing the key", errorStep(401, "Invalid API key: "+testKey, "bad key "+testKey)},
		{"an error reply without a message", rawStep(500, echo)},
		{"a 2xx that is not JSON", rawStep(200, echo)},
		{"a 2xx of another shape", jsonStep(200, map[string]any{"other": echo})},
		{"a 2xx whose field has another type", jsonStep(200, map[string]any{"rooms": echo})},
		{"a 2xx a typed model cannot hold", jsonStep(200, map[string]any{"rooms": []any{map[string]any{"createdAt": echo}}})},
		{"a redirect to the key", step{status: 302, headers: map[string]string{"Location": "https://svc:" + pw + "@evil.test/?k=" + testKey}}},
	}
	for _, tc := range cases {
		c := newClient(t, scripted(tc.st), withRetries(0), func(cfg *Config) { cfg.BaseURL = "https://svc:" + pw + "@x.test" })
		_, err := c.ListRooms(ctxBG)
		if err == nil {
			t.Errorf("%s: no error", tc.name)
			continue
		}
		j, _ := json.Marshal(err)
		for _, view := range []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), string(j)} {
			if strings.Contains(view, testKey) || strings.Contains(view, pw) {
				t.Errorf("%s: %q", tc.name, view)
			}
		}
		walkForSecrets(t, tc.name, reflect.ValueOf(err), map[uintptr]bool{}, testKey, pw)
		for e := errors.Unwrap(err); e != nil; e = errors.Unwrap(e) {
			walkForSecrets(t, tc.name+" (wrapped)", reflect.ValueOf(e), map[uintptr]bool{}, testKey, pw)
		}
	}
}

// walkForSecrets reads every string reachable from v, unexported fields
// included, as a reflection-based reporter or dumper would.
func walkForSecrets(t *testing.T, name string, v reflect.Value, seen map[uintptr]bool, secrets ...string) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		for _, s := range secrets {
			if strings.Contains(v.String(), s) {
				t.Errorf("%s: a value reachable from the error holds %q: %q", name, s, v.String())
			}
		}
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return
		}
		if v.Kind() == reflect.Pointer {
			if seen[v.Pointer()] {
				return
			}
			seen[v.Pointer()] = true
		}
		walkForSecrets(t, name, v.Elem(), seen, secrets...)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			walkForSecrets(t, name, v.Field(i), seen, secrets...)
		}
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
			for _, s := range secrets {
				if strings.Contains(string(v.Bytes()), s) {
					t.Errorf("%s: bytes reachable from the error hold %q", name, s)
				}
			}
			return
		}
		for i := 0; i < v.Len(); i++ {
			walkForSecrets(t, name, v.Index(i), seen, secrets...)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			walkForSecrets(t, name, k, seen, secrets...)
			walkForSecrets(t, name, v.MapIndex(k), seen, secrets...)
		}
	}
}
