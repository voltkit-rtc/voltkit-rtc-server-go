package voltkit

// Independent adversarial verification of the 2026-10-31 fixes (Go 0.1.3): decisions D4 to D9 and
// spec 3.5, tried with inputs round_2026_10_31_test.go does not use. Written by a verifier who did
// not make the fixes; nothing here changes product code.

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const verifyGoldenSecret = "whsec_golden_current"

func signedHeader(body, secret string, ts int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s", ts, body)
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

// --- spec 3.5: a refused redirect whose body never comes, on the real transport -------------------

// rawServer answers every connection with head, then waits for the client to close it and reports
// how long that took.
func verifyRawServer(t *testing.T, head string) (origin string, closedAfter <-chan time.Duration) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	closed := make(chan time.Duration, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if line == "\r\n" {
						break
					}
				}
				if _, err := c.Write([]byte(head)); err != nil {
					return
				}
				start := time.Now()
				_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
				_, _ = c.Read(make([]byte, 1)) // EOF (or a reset) once the client closes
				closed <- time.Since(start)
			}(conn)
		}
	}()
	return "http://" + ln.Addr().String(), closed
}

func TestVerify1031_ARedirectWhoseBodyStallsIsRefusedAtOnceAndItsConnectionCloses(t *testing.T) {
	for _, tc := range []struct{ name, head string }{
		{"a declared body that never comes", "HTTP/1.1 302 Found\r\nLocation: https://elsewhere.test/v1/rooms\r\nContent-Length: 100\r\n\r\nMoved to "},
		{"a chunked body that never starts", "HTTP/1.1 307 Temporary Redirect\r\nLocation: https://elsewhere.test/v1/rooms\r\nTransfer-Encoding: chunked\r\n\r\n"},
		{"no length, read to the close", "HTTP/1.1 301 Moved Permanently\r\nLocation: https://elsewhere.test/v1/rooms\r\n\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin, closed := verifyRawServer(t, tc.head)
			c, err := NewStreamClient(Config{BaseURL: origin, APIKey: testKey, Timeout: 3 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			_, err = c.ListRooms(context.Background())
			elapsed := time.Since(start)
			e := asError(t, err)
			if e.Code != "redirected" || e.Retryable || !strings.Contains(e.Message, "to https://elsewhere.test/v1/rooms;") {
				t.Fatalf("got %s %d retryable=%v %q", e.Code, e.Status, e.Retryable, e.Message)
			}
			if elapsed > time.Second {
				t.Errorf("the refusal took %s: the body was waited for", elapsed)
			}
			select {
			case d := <-closed:
				if d > time.Second {
					t.Errorf("the connection stayed open %s after the refusal", d)
				}
			case <-time.After(3 * time.Second):
				t.Error("the server never saw the connection close")
			}
		})
	}
}

// --- D4: bodies the vectors do not carry -----------------------------------------------------------

func TestVerify1031_D4_OddBodies(t *testing.T) {
	ctx := context.Background()
	call := func(c *StreamClient) (any, error) {
		return c.CreateToken(ctx, CreateTokenParams{RoomID: "r", Identity: "u", Role: RoleViewer})
	}
	for _, tc := range []struct {
		name, raw string
		wantToken string // "" means refused
		wantWhat  string
	}{
		{"token null", `{"token":null}`, "", `the reply is not the expected shape: "token" is not a non-empty string`},
		{"token a space", `{"token":" "}`, " ", ""},
		{"token with a NUL", `{"token":"\u0000"}`, "\x00", ""},
		{"token, then Token empty", `{"token":"t","Token":""}`, "t", ""},
		{"Token empty, then token", `{"Token":"","token":"t"}`, "t", ""},
		{"Token only", `{"Token":"t"}`, "", `the reply is not the expected shape: "token" is not a non-empty string`},
		{"the same key twice, last wins as JSON.parse", `{"token":"","token":"t"}`, "t", ""},
		{"the same key twice, empty last", `{"token":"t","token":""}`, "", `the reply is not the expected shape: "token" is not a non-empty string`},
		{"whitespace only", "  \n", "", "the reply is not JSON:   \n"},
		{"object then junk", `{"token":"t"} x`, "", `the reply is not JSON: {"token":"t"} x`},
		{"two objects", `{"token":"t"}{"token":"t"}`, "", `the reply is not JSON: {"token":"t"}{"token":"t"}`},
		{"NaN", `{"token":NaN}`, "", `the reply is not JSON: {"token":NaN}`},
		{"a list wrapper", `[{"token":"t"}]`, "", "the reply is not the expected shape: it is not an object"},
		{"deeply nested", strings.Repeat("[", 20000), "", "the reply is not JSON: " + strings.Repeat("[", 100)},
		{"a BOM (Go's decoder, unlike fetch's text(), keeps it: not JSON)", "\ufeff{\"token\":\"t\"}", "", "the reply is not JSON: \ufeff{\"token\":\"t\"}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft := scripted(rawStep(200, tc.raw))
			c := newClient(t, ft)
			got, err := call(c)
			if tc.wantToken != "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if tok := got.(*RoomToken).Token; tok != tc.wantToken {
					t.Fatalf("token %q, want %q", tok, tc.wantToken)
				}
				return
			}
			e := expectError(t, err, "http-200", 200, false, "POST /v1/rooms/r/tokens: "+tc.wantWhat)
			if ft.count() != 1 {
				t.Errorf("%d attempts, want 1 (%v)", ft.count(), e)
			}
		})
	}
}

func TestVerify1031_D4_ACaseVariantOfAnUncheckedFieldIsDecodedLastWins(t *testing.T) {
	// The check reads roomId exactly and the decoder is told to keep the exact roomId; every other
	// field still goes through encoding/json's case-insensitive, last-wins matching. Node would hand
	// back token "t" here. The controller never spells a key two ways: recorded as a divergence.
	ft := scripted(rawStep(200, `{"roomId":"r","hostToken":"t","HostToken":"","wsUrl":"wss://x"}`))
	c := newClient(t, ft)
	room, err := c.EnsureRoom(context.Background(), EnsureRoomParams{RoomID: "r", HostIdentity: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if room.RoomID != "r" {
		t.Errorf("roomId %q", room.RoomID)
	}
	if room.HostToken != "t" {
		t.Logf("divergence from Node: hostToken decoded as %q for a reply spelling it twice (hostToken, HostToken)", room.HostToken)
	}
}

func TestVerify1031_D4_ExistedIsOverwrittenAfterTheCheck(t *testing.T) {
	ctx := context.Background()
	c := newClient(t, scripted(rawStep(201, `{"roomId":"r","token":"t","existed":true}`)))
	room, err := c.EnsureRoom(ctx, EnsureRoomParams{RoomID: "r", HostIdentity: "h"})
	if err != nil || room.Existed {
		t.Fatalf("existed=%v err=%v", room != nil && room.Existed, err)
	}
	c = newClient(t, scripted(rawStep(200, `{"existed":false,"token":"t"}`)))
	_, err = c.EnsureRoom(ctx, EnsureRoomParams{RoomID: "r", HostIdentity: "h"})
	expectError(t, err, "http-200", 200, false, `PUT /v1/rooms/r: the reply is not the expected shape: "roomId" is not a non-empty string`)
}

func TestVerify1031_D4_ListFieldsAndObjectFields(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		raw  string
		call func(c *StreamClient) error
		want string
	}{
		{`{"participants":{"0":{}}}`, func(c *StreamClient) error { _, err := c.ListParticipants(ctx, "r"); return err }, `GET /v1/rooms/r/participants: the reply is not the expected shape: "participants" is not an array`},
		{`{"participants":null}`, func(c *StreamClient) error { _, err := c.ListParticipants(ctx, "r"); return err }, `GET /v1/rooms/r/participants: the reply is not the expected shape: "participants" is not an array`},
		{`{"participants":"[]"}`, func(c *StreamClient) error { _, err := c.ListParticipants(ctx, "r"); return err }, `GET /v1/rooms/r/participants: the reply is not the expected shape: "participants" is not an array`},
		{`{"room":[]}`, func(c *StreamClient) error { _, err := c.GetRoom(ctx, "r"); return err }, `GET /v1/rooms/r: the reply is not the expected shape: "room" is not an object`},
		{`{"room":null}`, func(c *StreamClient) error { _, err := c.GetRoom(ctx, "r"); return err }, `GET /v1/rooms/r: the reply is not the expected shape: "room" is not an object`},
		{`{"rooms":{}}`, func(c *StreamClient) error { _, err := c.ListRooms(ctx); return err }, `GET /v1/rooms: the reply is not the expected shape: "rooms" is not an array`},
		{`[]`, func(c *StreamClient) error { _, err := c.ListRooms(ctx); return err }, `GET /v1/rooms: the reply is not the expected shape: it is not an object`},
		{`{"data":[]}`, func(c *StreamClient) error { _, err := c.GetRates(ctx); return err }, ""},
	} {
		ft := scripted(rawStep(200, tc.raw))
		c := newClient(t, ft, withRates("https://api.example.com"))
		err := tc.call(c)
		e := asError(t, err)
		if e.Code != "http-200" || e.Status != 200 || e.Retryable || ft.count() != 1 {
			t.Errorf("%s: %s %d retryable=%v attempts=%d", tc.raw, e.Code, e.Status, e.Retryable, ft.count())
		}
		if tc.want != "" && e.Message != tc.want {
			t.Errorf("%s: %q", tc.raw, e.Message)
		}
		if strings.Contains(e.Message, tc.raw) && tc.want != "" {
			t.Errorf("%s: the message quotes the reply: %q", tc.raw, e.Message)
		}
	}
	// A participants list that is present and odd inside is the model's business, not the check's.
	c := newClient(t, scripted(rawStep(200, `{"Participants":[],"participants":[{"identity":"u"}]}`)))
	list, err := c.ListParticipants(ctx, "r")
	if err != nil || len(list) != 1 {
		t.Errorf("list=%v err=%v", list, err)
	}
}

func TestVerify1031_D4_AHugeListIsReadWhole(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"participants":[`)
	for i := 0; i < 100000; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"identity":"u%d","joinedAt":%d}`, i, i)
	}
	b.WriteString("]}")
	c := newClient(t, scripted(rawStep(200, b.String())))
	start := time.Now()
	list, err := c.ListParticipants(context.Background(), "r")
	if err != nil || len(list) != 100000 {
		t.Fatalf("len=%d err=%v", len(list), err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %s", d)
	}
}

// --- D5: webhooks at the edges --------------------------------------------------------------------

func TestVerify1031_D5_ClockExtremesAndTolerances(t *testing.T) {
	body, header := goldenBody, "t=1787000000,v1="+goldenCurrent
	clock := func(tm time.Time, tol *time.Duration) *VerifyOptions {
		return &VerifyOptions{Now: func() time.Time { return tm }, Tolerance: tol}
	}
	zero := time.Duration(0)
	for _, tc := range []struct {
		name string
		opts *VerifyOptions
		want bool
	}{
		{"tolerance 0, the last nanosecond of the second", clock(time.Unix(goldenTS, 999_999_999), &zero), true},
		{"tolerance 0, a nanosecond before the second", clock(time.Unix(goldenTS-1, 999_999_999), &zero), false},
		{"tolerance 0, the next second", clock(time.Unix(goldenTS+1, 0), &zero), false},
		{"the zero time", clock(time.Time{}, nil), false},
		{"a time far ahead", clock(time.Unix(1<<62, 0), nil), false},
		{"a time far behind", clock(time.Unix(-(1<<62), 0), nil), false},
		{"the longest tolerance from the zero time", clock(time.Time{}, Ptr(time.Duration(1<<63-1))), false},
		{"the longest tolerance from 2001", clock(time.Unix(1e9, 0), Ptr(time.Duration(1<<63-1))), true},
		{"a fractional tolerance counts its whole seconds", clock(time.Unix(goldenTS+1, 0), Ptr(1500*time.Millisecond)), true},
		{"a fractional tolerance, one more second", clock(time.Unix(goldenTS+2, 0), Ptr(1500*time.Millisecond)), false},
	} {
		if got := VerifyWebhook([]byte(body), header, []string{verifyGoldenSecret}, tc.opts); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestVerify1031_D5_SecretsAndHeadersAtTheEdges(t *testing.T) {
	body := goldenBody
	header := "t=1787000000,v1=" + goldenCurrent
	opts := at(goldenTS)
	// JavaScript's trim set, each alone: absent.
	for _, blank := range []string{" ", "\t", "\n", "\r", "\f", "\v", "\u00a0", "\u1680", "\u2003", "\u2028", "\u2029", "\u202f", "\u205f", "\u3000", "\ufeff", " \u00a0\ufeff\t"} {
		signed := signedHeader(body, blank, goldenTS)
		if VerifyWebhook([]byte(body), signed, []string{blank, verifyGoldenSecret}, opts) {
			t.Errorf("%q verified a delivery signed with it", blank)
		}
		if !VerifyWebhook([]byte(body), header, []string{blank, verifyGoldenSecret}, opts) {
			t.Errorf("%q beside the real secret stopped the real one", blank)
		}
		if _, err := ParseWebhook([]byte(body), signed, []string{blank, "", "  "}, opts); err == nil || !strings.Contains(err.Error(), "needs the signing secret") {
			t.Errorf("%q alone: %v", blank, err)
		}
	}
	// Beyond JavaScript's set: U+0085 is whitespace to unicode.IsSpace, not to trim(). Recorded, not pinned.
	if _, err := ParseWebhook([]byte(body), signedHeader(body, "\u0085", goldenTS), []string{"\u0085"}, opts); err != nil {
		t.Logf("divergence from Node: a secret of only U+0085 is absent here (%v); Node would verify with it", err)
	}
	padded := " " + verifyGoldenSecret + "\n"
	if VerifyWebhook([]byte(body), header, []string{padded}, opts) {
		t.Error("a padded secret verified the unpadded signature")
	}
	if !VerifyWebhook([]byte(body), signedHeader(body, padded, goldenTS), []string{padded}, opts) {
		t.Error("a padded secret is the whole string")
	}
	ok := func(h string) bool { return VerifyWebhook([]byte(body), h, []string{verifyGoldenSecret}, opts) }
	for h, want := range map[string]bool{
		" t=1787000000 , v1=" + goldenCurrent + " ":                           true,
		"v1=" + goldenCurrent + ",t=1787000000":                               true,
		"t=1787000000,v0=zzz,v1=" + goldenCurrent:                             true,
		"t=1787000000,v1=" + strings.Repeat("0", 64) + ",v1=" + goldenCurrent: true,
		"t=1787000000,v1=" + strings.Repeat("Z", 64) + ",v1=" + goldenCurrent: false,
		"t=1787000000,t=1787000000,v1=" + goldenCurrent:                       false,
		"t=1787000000,v1=" + goldenCurrent + ",":                              false,
		"t=1787000000,v1=" + goldenCurrent + ",=x":                            false,
		"t=1787000000,v1=" + strings.ToUpper(goldenCurrent):                   false,
		"t=01787000000,v1=" + goldenCurrent:                                   false,
		"t=+1787000000,v1=" + goldenCurrent:                                   false,
		"t=1787000000;v1=" + goldenCurrent:                                    false,
		"t=1787000000,v1=" + goldenCurrent + strings.Repeat(",", 5000):        false,
		"": false,
	} {
		if got := ok(h); got != want {
			t.Errorf("%q: %v, want %v", h, got, want)
		}
	}
	// A part padded with U+FEFF: Node's trim() takes it, strings.TrimSpace does not. Recorded.
	if !ok("\ufefft=1787000000,v1=" + goldenCurrent + "\u00a0") {
		t.Log("divergence from Node: a header part padded with U+FEFF or NBSP is refused here, accepted by Node's trim()")
	}
}

func TestVerify1031_D5_ParseOnBodiesThatAreNotAnEvent(t *testing.T) {
	opts := at(goldenTS)
	for raw, wantErr := range map[string]bool{"[]": true, `"s"`: true, "5": true, "{": true, "\ufeff{}": true, "null": false, `{"0":1}`: false, "{}": false} {
		event, err := ParseWebhook([]byte(raw), signedHeader(raw, verifyGoldenSecret, goldenTS), []string{verifyGoldenSecret}, opts)
		if (err != nil) != wantErr {
			t.Errorf("%s: event=%+v err=%v", raw, event, err)
			continue
		}
		if err == nil && event == nil {
			t.Errorf("%s: a nil event with no error", raw)
		}
		var e *Error
		if err != nil && errors.As(err, &e) {
			t.Errorf("%s: a verified body that is no event must not be a *Error: %v", raw, err)
		}
	}
	// Recorded: a verified `null` is an empty event here (Type ""), where Java refuses it and Node returns null.
	event, err := ParseWebhook([]byte("null"), signedHeader("null", verifyGoldenSecret, goldenTS), []string{verifyGoldenSecret}, opts)
	if err == nil && event != nil && event.Type == "" {
		t.Logf("divergence: ParseWebhook of a verified null body returns an empty event (%+v), no error", *event)
	}
}

// --- D6: ids: Go has only strings, so "", "." and ".." and the encoder are what is left ------------

func TestVerify1031_D6_DotSegmentsInEverySpelling(t *testing.T) {
	ctx := context.Background()
	for id, want := range map[string]string{
		"%2e%2e": "%252e%252e", "%2E%2E": "%252E%252E", "a/..": "a%2F..", "../": "..%2F", " ..": "%20..",
		"..\x00": "..%00", "\u2024\u2024": "%E2%80%A4%E2%80%A4", "?": "%3F", "#": "%23", "a@b": "a%40b", "\\": "%5C",
		"é": "%C3%A9", "!*'()": "!*'()", "$&+,;=:@": "%24%26%2B%2C%3B%3D%3A%40",
	} {
		ft := scripted(rawStep(200, `{"room":{}}`))
		c := newClient(t, ft)
		if _, err := c.GetRoom(ctx, id); err != nil {
			t.Errorf("%q: %v", id, err)
			continue
		}
		if got := ft.call(0).URL; got != "https://x.test/v1/rooms/"+want {
			t.Errorf("%q: %s", id, got)
		}
	}
	for _, id := range []string{"", ".", ".."} {
		ft := scripted(rawStep(200, `{"room":{}}`))
		c := newClient(t, ft)
		_, err := c.GetRoom(ctx, id)
		e := asError(t, err)
		if e.Code != "bad-request" || e.Status != 0 || ft.count() != 0 {
			t.Errorf("%q: %v (%d sent)", id, err, ft.count())
		}
	}
}

// --- D7: URL credentials, in every channel Go has, and the clock -------------------------------

func TestVerify1031_D7_CredentialShapes(t *testing.T) {
	for text, want := range map[string]string{
		"https://u:p@[::1]:8/x":                  "https://[redacted]@[::1]:8/x",
		"HTTPS://User:Pass@H.test/":              "HTTPS://[redacted]@H.test/",
		"scheme://@host/x":                       "scheme://[redacted]@host/x",
		"https://user@host/x":                    "https://[redacted]@host/x",
		"user@host":                              "user@host",
		"https://h/p?next=https://u:p@ss@evil/x": "https://h/p?next=https://[redacted]@evil/x",
		"https://h?e=a@b":                        "https://h?e=a@b",
		"https://user:p@ssword@h":                "https://[redacted]@h",
		"https://user:hunter2/x@h":               "https://[redacted]@h",
		"https://user:pa?ss@h":                   "https://[redacted]@h",
		"https://user:pa#ss@h":                   "https://[redacted]@h",
		"https://user:123/x@h":                   "https://user:123/x@h",
		"https://user:123@h":                     "https://[redacted]@h",
		"https://user:@h":                        "https://[redacted]@h",
		"https://:pw@h":                          "https://[redacted]@h",
		"mailto:me@host":                         "mailto:me@host",
		"1https://u:p@h":                         "1https://[redacted]@h",
		"x1https://u:p@h":                        "x1https://[redacted]@h",
		"+a://u:p@h":                             "+a://[redacted]@h",
		"https://u:p@h https://v:q@i":            "https://[redacted]@h https://[redacted]@i",
		"https://u:p@h\u00a0https://v:q@i":       "https://[redacted]@h\u00a0https://[redacted]@i",
		"see https://u:p@h.":                     "see https://[redacted]@h.",
		"(https://u:p@h)":                        "(https://[redacted]@h)",
		"https://u:p@h@i":                        "https://[redacted]@i",
		"https://a@b:c@d":                        "https://[redacted]@d",
		"https://u:p/q@h@i":                      "https://[redacted]@i",
		"https://u:p/q@h/@i":                     "https://[redacted]@i",
		"https://u:p@h/x?y=1#z@w":                "https://[redacted]@h/x?y=1#z@w",
		"https://h/x@y:z@w":                      "https://h/x@y:z@w",
		"https://user:p%40ss@h":                  "https://[redacted]@h",
		"https://u:p@h\nhttps://v:q@i":           "https://[redacted]@h\nhttps://[redacted]@i",
		"https://@":                              "https://[redacted]@",
		"@https://u:p@h":                         "@https://[redacted]@h",
		"https://u:p@h@":                         "https://[redacted]@",
		"a://b://c@":                             "a://b://[redacted]@",
		"https://u:p@h\u0085https://v:q@i":       "https://[redacted]@h\u0085https://[redacted]@i", // U+0085 is no JS word break; the first form stops at the last @ before the /
		"https://u:12/x@h https://v:p@i":         "https://u:12/x@h https://[redacted]@i",
		"https://u:p@h/\u3000https://v:q@i":      "https://[redacted]@h/\u3000https://[redacted]@i",
	} {
		if got := redactURLCredentials(text); got != want {
			t.Errorf("%q:\n got %q\nwant %q", text, got, want)
		}
	}
}

func TestVerify1031_D7_AdversarialPagesStayFast(t *testing.T) {
	c := newClient(t, scripted())
	pages := map[string]string{
		"a://b:c repeated, one @":        strings.Repeat("a://b:c", 43000) + "@",
		"a://b:c@ repeated":              strings.Repeat("a://b:c@", 37000),
		"only @":                         strings.Repeat("@", 300000),
		"a: repeated then ://x@":         strings.Repeat("a:", 150000) + "://x@",
		"a://b:1/ repeated, one @":       strings.Repeat("a://b:1/", 37000) + "@",
		"a:// repeated, one @":           strings.Repeat("a://", 75000) + "@",
		"://: repeated":                  strings.Repeat("://:", 75000) + "@",
		"a long scheme-like run then @":  strings.Repeat("a", 300000) + "://u:p@h",
		"zeros in a reference":           "&#" + strings.Repeat("0", 300000) + "95;",
		"the key's first character":      strings.Repeat(string(testKey[0]), 300000),
		"near misses of the key":         strings.Repeat(testKey[:len(testKey)-1]+" ", 6500),
		"%73 repeated":                   strings.Repeat("%73", 100000),
		"@ and : alternating":            strings.Repeat("@:", 150000),
		"words with an @ each":           strings.Repeat("u:p@h ", 50000),
		"digits after every colon":       strings.Repeat("a://b:1", 43000) + "@",
		"many schemes in one word, no @": strings.Repeat("a://", 75000),
	}
	for name, page := range pages {
		start := time.Now()
		out := c.s.redact(page)
		if d := time.Since(start); d > time.Second {
			t.Errorf("%s: %s", name, d)
		}
		if strings.Contains(out, testKey) {
			t.Errorf("%s: the key survived", name)
		}
	}
}

func TestVerify1031_D7_EveryFmtVerbAndEncoderOnTheClientAndConfig(t *testing.T) {
	cfg := Config{BaseURL: "https://deploy:hunter2@x.test/", APIKey: testKey, RatesBaseURL: "https://r:pw@api.test"}
	c, err := NewStreamClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	type wrapped struct {
		C   *StreamClient
		Cfg Config
		c   *StreamClient
		cfg Config
	}
	w := wrapped{C: c, Cfg: cfg, c: c, cfg: cfg}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%T"} {
		for name, v := range map[string]any{"client": c, "*client": *c, "config": cfg, "&config": &cfg, "wrapped": w, "&wrapped": &w, "[]any": []any{c, cfg}} {
			if name == "wrapped" || name == "&wrapped" {
				// fmt reaches cfg through an unexported field and calls no method there: Config says so
				// in its doc (G012-5, documented, not fixable). The client beside it must still give
				// nothing away.
				s := fmt.Sprintf(verb, v)
				if strings.Contains(s, testKey) && !strings.Contains(verb, "T") {
					t.Logf("%s of %s shows the key through the unexported Config field (documented in Config)", verb, name)
				}
				continue
			}
			if s := fmt.Sprintf(verb, v); strings.Contains(s, testKey) || strings.Contains(s, "hunter2") || strings.Contains(s, ":pw@") {
				t.Errorf("%s of %s: %s", verb, name, s)
			}
		}
	}
	for name, v := range map[string]any{"client": c, "config": cfg, "exported fields": struct {
		C   *StreamClient
		Cfg Config
	}{c, cfg}} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Errorf("json of %s: %v", name, err)
		}
		if strings.Contains(string(b), testKey) || strings.Contains(string(b), "hunter2") {
			t.Errorf("json of %s: %s", name, b)
		}
	}
	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("x", "client", c, "config", cfg, slog.Any("c2", c))
	if s := buf.String(); strings.Contains(s, testKey) || strings.Contains(s, "hunter2") {
		t.Errorf("slog: %s", s)
	}
	// Reflection over the client finds only function values where the secrets are.
	v := reflect.ValueOf(c).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() == reflect.String && (f.String() == testKey || strings.Contains(f.String(), "hunter2")) {
			t.Errorf("field %s holds a secret as a string", v.Type().Field(i).Name)
		}
	}
	sv := reflect.ValueOf(c.s).Elem()
	for i := 0; i < sv.NumField(); i++ {
		if sv.Field(i).Kind() != reflect.Func {
			t.Errorf("secrets.%s is a %s, readable by a dumper", sv.Type().Field(i).Name, sv.Field(i).Kind())
		}
	}
}

func TestVerify1031_D9_WrappedErrorsPrintNoSecret(t *testing.T) {
	// A transport error whose words quote the URL (password and all) and the key, wrapped by the
	// caller: %+v, %#v and errors.Unwrap show only redacted text.
	c, err := NewStreamClient(Config{BaseURL: "https://deploy:hunter2@x.test", APIKey: testKey, Retries: Ptr(0), HTTPClient: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("dial %s failed; header was %s", r.URL.String(), r.Header.Get("X-API-Key"))
	})}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ListRooms(context.Background())
	outer := fmt.Errorf("outer: %w", err)
	for _, s := range []string{err.Error(), outer.Error(), fmt.Sprintf("%+v", outer), fmt.Sprintf("%#v", outer), fmt.Sprintf("%#v", err), fmt.Sprintf("%+v", asError(t, err))} {
		if strings.Contains(s, testKey) || strings.Contains(s, "hunter2") {
			t.Errorf("%s", s)
		}
	}
	if !strings.Contains(err.Error(), "https://[redacted]@x.test/v1/rooms") {
		t.Errorf("the URL is not shown redacted: %s", err)
	}
	if u := errors.Unwrap(asError(t, err)); u != nil {
		t.Errorf("an *Error wraps %v", u)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// --- D8: user info of every shape, on the real transport -------------------------------------------

func TestVerify1031_D8_UserInfoShapesOnTheWire(t *testing.T) {
	var mu sync.Mutex
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = r.Header.Clone()
		mu.Unlock()
		_, _ = w.Write([]byte(`{"rooms":[]}`))
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	for userinfo, basic := range map[string]string{"": "", "@": ":", "user@": "user:", "user:@": "user:", ":pw@": ":pw", "u%3Ax:p%40w@": "u:x:p@w", "u:p%2Fq%3Fr%23s@": "u:p/q?r#s"} {
		c, err := NewStreamClient(Config{BaseURL: "http://" + userinfo + host, APIKey: testKey})
		if err != nil {
			t.Errorf("%q refused: %v", userinfo, err)
			continue
		}
		if _, err := c.ListRooms(context.Background()); err != nil {
			t.Errorf("%q: %v", userinfo, err)
			continue
		}
		mu.Lock()
		got := seen.Get("Authorization")
		mu.Unlock()
		want := ""
		if basic != "" {
			want = "Basic " + base64.StdEncoding.EncodeToString([]byte(basic))
		}
		if got != want {
			t.Errorf("%q: Authorization %q, want %q", userinfo, got, want)
		}
		if shown := fmt.Sprintf("%v", c); strings.Contains(shown, "pw") || strings.Contains(shown, "p%40w") {
			t.Errorf("%q printed as %s", userinfo, shown)
		}
	}
	for _, url := range []string{"https://x.test/%zz", "https://x.test:", "https://[::1", "https://u:p@", "https://u:p@:1", "https://x.test:٤٤٣", "https://x.test:+1", "https://x.test:1:1"} {
		for _, cfg := range []Config{{BaseURL: url, APIKey: testKey}, {BaseURL: "https://x.test", RatesBaseURL: url, APIKey: testKey}} {
			_, err := NewStreamClient(cfg)
			if err == nil {
				t.Errorf("%q accepted", url)
				continue
			}
			if strings.Contains(err.Error(), "x.test") && !strings.Contains(err.Error(), "api.voltkit.in") || strings.Contains(err.Error(), "u:p") || errors.Unwrap(err) != nil {
				t.Errorf("%q: %v", url, err)
			}
		}
	}
}
