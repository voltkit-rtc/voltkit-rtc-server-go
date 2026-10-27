package voltkit

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The reply size cap (0.1.1, from the testers' hardening note of 2026-10-27):
// a reply body is read up to the cap and no further, on a 2xx and on an error
// status alike, so a misbehaving API or a proxy in front of it cannot make
// the SDK allocate without bound. A reply past the cap fails with
// http-<status> and a message that says so; the server's words are not used.

const tooLargeText = "the reply is too large: over 8 MiB, the most this SDK reads"

// paddedRooms is a valid rooms reply of exactly n bytes: one room, then an
// unknown field the decoder skips, so a reply at the cap costs the decoder
// nothing but the scan.
func paddedRooms(n int) string {
	head, tail := `{"rooms":[{"roomId":"r"}],"pad":"`, `"}`
	return head + strings.Repeat("m", n-len(head)-len(tail)) + tail
}

func TestReply_AStreamedReplyPastTheCapIsRefusedAndNotReadToTheEnd(t *testing.T) {
	// Four times the cap, chunked (no Content-Length), flushed as it goes:
	// the client has to stop reading on its own.
	const total = 4 * maxReplyBytes
	var mu sync.Mutex
	written := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		body := paddedRooms(total)
		chunk := 64 << 10
		for off := 0; off < len(body); off += chunk {
			n, err := w.Write([]byte(body[off:min(off+chunk, len(body))]))
			mu.Lock()
			written += n
			mu.Unlock()
			if err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()
	c := liveClient(t, srv.URL, withRetries(0), func(cfg *Config) { cfg.Timeout = 10 * time.Second })
	started := time.Now()
	rooms, err := c.ListRooms(ctxBG)
	within(t, started, 10*time.Second, "a reply past the cap")
	if err == nil {
		t.Fatalf("a %d MiB reply was read whole and decoded (%d rooms); want it refused at the cap", total>>20, len(rooms.Rooms))
	}
	expectError(t, err, "http-200", 200, false, "GET /v1/rooms: "+tooLargeText)
	srv.Close() // waits for the handler, so the count is final
	mu.Lock()
	defer mu.Unlock()
	if written >= total {
		t.Errorf("the server wrote all %d bytes: the client kept reading past the cap", written)
	}
}

func TestReply_ADeclaredLengthPastTheCapIsRefusedBeforeReading(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000000")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"rooms":`))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := liveClient(t, srv.URL, withRetries(0), func(cfg *Config) { cfg.Timeout = 5 * time.Second })
	started := time.Now()
	_, err := c.ListRooms(ctxBG)
	expectError(t, err, "http-200", 200, false, "GET /v1/rooms: "+tooLargeText)
	within(t, started, 2*time.Second, "a declared length past the cap")
}

func TestReply_AtTheCapIsReadAndOneByteOverIsNot(t *testing.T) {
	t.Run("exactly at the cap", func(t *testing.T) {
		ft := scripted(rawStep(200, paddedRooms(maxReplyBytes)))
		rooms, err := newClient(t, ft).ListRooms(ctxBG)
		if err != nil || len(rooms.Rooms) != 1 || rooms.Rooms[0].RoomID != "r" {
			t.Fatalf("a reply at the cap must be read and decoded: %v, %v", rooms, err)
		}
	})
	t.Run("one byte over", func(t *testing.T) {
		ft := scripted(rawStep(200, paddedRooms(maxReplyBytes+1)))
		_, err := newClient(t, ft, withRetries(2)).ListRooms(ctxBG)
		expectError(t, err, "http-200", 200, false, "GET /v1/rooms: "+tooLargeText)
		if ft.count() != 1 {
			t.Errorf("a 2xx past the cap is not retried: %d requests", ft.count())
		}
	})
	t.Run("one byte over, with the key in it", func(t *testing.T) {
		body := paddedRooms(maxReplyBytes + 1)
		body = body[:40] + testKey + body[40+len(testKey):]
		ft := scripted(rawStep(200, body))
		_, err := newClient(t, ft).ListRooms(ctxBG)
		e := expectError(t, err, "http-200", 200, false, "")
		if strings.Contains(e.Message, testKey) || strings.Contains(e.Message, "mmmm") {
			t.Errorf("nothing of the reply may reach the message: %q", e.Message)
		}
	})
}

func TestReply_ErrorBodiesUnderTheCapAreCutShortAsBeforeAndPastItRefused(t *testing.T) {
	t.Run("a 5 MB error body is read, redacted and cut to 300 characters", func(t *testing.T) {
		body := "bad key " + testKey + strings.Repeat("y", 5<<20)
		ft := scripted(rawStep(503, body))
		_, err := newClient(t, ft, withRetries(0)).ListRooms(ctxBG)
		want := "GET /v1/rooms: " + cutRunes("bad key [redacted]"+strings.Repeat("y", 5<<20), 300)
		e := expectError(t, err, "http-503", 503, true, want)
		if strings.Contains(e.Message, testKey) {
			t.Errorf("the key reached the message: %q", e.Message)
		}
	})
	t.Run("a 503 past the cap keeps the status rule: retryable, and the budget is spent", func(t *testing.T) {
		ft := scripted(rawStep(503, "x"+paddedRooms(maxReplyBytes)))
		_, err := newClient(t, ft, withRetries(2)).ListRooms(ctxBG)
		expectError(t, err, "http-503", 503, true, "GET /v1/rooms: "+tooLargeText)
		if ft.count() != 3 {
			t.Errorf("a retryable status past the cap uses the retry budget: %d requests", ft.count())
		}
	})
	t.Run("a 400 past the cap is a decision the server made: not retried", func(t *testing.T) {
		ft := scripted(rawStep(400, `{"error":"bad-request","message":"`+strings.Repeat("z", maxReplyBytes)+`"}`))
		_, err := newClient(t, ft, withRetries(2)).ListRooms(ctxBG)
		expectError(t, err, "http-400", 400, false, "GET /v1/rooms: "+tooLargeText)
		if ft.count() != 1 {
			t.Errorf("%d requests", ft.count())
		}
	})
}
