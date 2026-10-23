package voltkit

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The real transport against loopback servers: the deadline over a body that
// stalls after its headers (Node test NODE-01), a body that breaks off
// (NODE-02), redirects on a keyed call and on the rates call, a success that
// is not JSON, and what reaches the wire.

type loopback struct {
	*httptest.Server
	hits atomic.Int32
}

func serve(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, hit int)) *loopback {
	t.Helper()
	lb := &loopback{}
	lb.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler(w, r, int(lb.hits.Add(1)))
	}))
	t.Cleanup(lb.Close)
	return lb
}

func liveClient(t *testing.T, baseURL string, modify ...func(*Config)) *StreamClient {
	t.Helper()
	cfg := Config{BaseURL: baseURL, APIKey: testKey}
	for _, m := range modify {
		m(&cfg)
	}
	c, err := NewStreamClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pinClock(c)
	return c
}

// stallBody writes the headers and the start of a body that never finishes,
// until the client goes away.
func stallBody(w http.ResponseWriter, r *http.Request, _ int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write([]byte(`{"rooms":`))
	w.(http.Flusher).Flush()
	<-r.Context().Done()
}

func TestTransport_ABodyThatStallsAfterTheHeadersFailsWithTheTimeout(t *testing.T) {
	srv := serve(t, stallBody)
	c := liveClient(t, srv.URL, func(cfg *Config) { cfg.Timeout = 150 * time.Millisecond }, withRetries(0))
	started := time.Now()
	_, err := c.ListRooms(ctxBG)
	expectError(t, err, "network", 0, true, "GET /v1/rooms: timed out after 150ms")
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Errorf("took %s: the deadline must cover the body", elapsed)
	}
	if srv.hits.Load() != 1 {
		t.Errorf("%d hits", srv.hits.Load())
	}
}

func TestTransport_AStalledBodyUsesTheRetryBudgetOnlyWhenSafe(t *testing.T) {
	srv := serve(t, stallBody)
	c := liveClient(t, srv.URL, func(cfg *Config) { cfg.Timeout = 100 * time.Millisecond }, withRetries(1))
	_, err := c.ListRooms(ctxBG)
	expectError(t, err, "network", 0, true, "GET /v1/rooms: timed out after 100ms")
	if srv.hits.Load() != 2 {
		t.Errorf("%d hits, want 2: a timeout is transient and ListRooms is safe to repeat", srv.hits.Load())
	}
	_, err = c.SendMessage(ctxBG, SendMessageParams{Channel: "c", Text: "hi"})
	expectError(t, err, "network", 0, true, "POST /v1/chat/c/messages: timed out after 100ms")
	if srv.hits.Load() != 3 {
		t.Errorf("SendMessage is never repeated: %d hits", srv.hits.Load()-2)
	}
}

func TestTransport_ASlowBodyThatFinishesInTimeSucceeds(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"rooms":`))
		w.(http.Flusher).Flush()
		time.Sleep(60 * time.Millisecond)
		_, _ = w.Write([]byte(`[]}`))
	})
	list, err := liveClient(t, srv.URL, func(cfg *Config) { cfg.Timeout = 2 * time.Second }).ListRooms(ctxBG)
	if err != nil || len(list.Rooms) != 0 {
		t.Errorf("%v %v", list, err)
	}
}

func TestTransport_AConnectionDroppedMidBodyIsRetried(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request, hit int) {
		w.Header().Set("Content-Type", "application/json")
		if hit == 1 {
			// Announce a longer body than is sent, then drop the connection.
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"rooms":`))
			w.(http.Flusher).Flush()
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		_, _ = w.Write([]byte(`{"rooms":[]}`))
	})
	list, err := liveClient(t, srv.URL, func(cfg *Config) { cfg.Timeout = 2 * time.Second }).ListRooms(ctxBG)
	if err != nil || len(list.Rooms) != 0 {
		t.Fatalf("%v %v", list, err)
	}
	if srv.hits.Load() != 2 {
		t.Errorf("%d hits, want 2", srv.hits.Load())
	}
}

func TestTransport_ARedirectWithTheKeyIsNeitherFollowedNorRetried(t *testing.T) {
	var keyReachedTarget atomic.Bool
	target := serve(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		if r.Header.Get("X-API-Key") != "" {
			keyReachedTarget.Store(true)
		}
		_, _ = w.Write([]byte(`{"rooms":[]}`))
	})
	srv := serve(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	})
	for _, do := range []func(c *StreamClient) error{
		func(c *StreamClient) error { _, err := c.ListRooms(ctxBG); return err },
		func(c *StreamClient) error {
			_, err := c.CreateRoom(ctxBG, CreateRoomParams{HostIdentity: "h"})
			return err
		},
		func(c *StreamClient) error { return c.DeleteRoom(ctxBG, "r") },
	} {
		err := do(liveClient(t, srv.URL))
		e := expectError(t, err, "redirected", 302, false, "")
		if !strings.Contains(e.Message, "answered 302 to "+target.URL) {
			t.Errorf("%q", e.Message)
		}
	}
	if srv.hits.Load() != 3 || target.hits.Load() != 0 || keyReachedTarget.Load() {
		t.Errorf("origin hits %d, target hits %d, key reached target %v", srv.hits.Load(), target.hits.Load(), keyReachedTarget.Load())
	}
}

func TestTransport_TheRatesCallFollowsARedirectWithoutACredential(t *testing.T) {
	var sawCredential atomic.Bool
	target := serve(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		if r.Header.Get("X-API-Key") != "" || r.Header.Get("Authorization") != "" {
			sawCredential.Store(true)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"version":5,"kind":"standard"}}`))
	})
	srv := serve(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusMovedPermanently)
	})
	rates, err := liveClient(t, "https://x.test", withRates(srv.URL)).GetRates(ctxBG)
	if err != nil || rates.Version != 5 {
		t.Fatalf("%v %v", rates, err)
	}
	if target.hits.Load() != 1 || sawCredential.Load() {
		t.Errorf("target hits %d, credential %v", target.hits.Load(), sawCredential.Load())
	}
}

func TestTransport_ASuccessThatIsNotJSON(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><html>a proxy page</html>"))
	})
	_, err := liveClient(t, srv.URL).GetBalance(ctxBG)
	expectError(t, err, "http-200", 200, false, "GET /v1/billing/balance: the reply is not JSON: <!doctype html><html>a proxy page</html>")
	if srv.hits.Load() != 1 {
		t.Errorf("%d hits: not retried", srv.hits.Load())
	}
}

func TestTransport_WhatReachesTheWire(t *testing.T) {
	var seen struct {
		uri, key, ua, ct string
	}
	srv := serve(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		seen.uri, seen.key, seen.ua, seen.ct = r.RequestURI, r.Header.Get("X-API-Key"), r.Header.Get("User-Agent"), r.Header.Get("Content-Type")
		_, _ = w.Write([]byte(`{}`))
	})
	c := liveClient(t, srv.URL, func(cfg *Config) { cfg.APIKey = " " + testKey + "\n" })
	if _, err := c.GetRoom(ctxBG, "a b/c"); err != nil {
		t.Fatal(err)
	}
	if seen.uri != "/v1/rooms/a%20b%2Fc" || seen.key != testKey || seen.ua != "voltkit-rtc-go/"+Version || seen.ct != "" {
		t.Errorf("%+v", seen)
	}
	if _, err := c.ListMessages(ctxBG, "c", &ListMessagesOptions{Limit: 5, Before: "1-0"}); err != nil {
		t.Fatal(err)
	}
	if seen.uri != "/v1/chat/c/messages?limit=5&before=1-0" {
		t.Errorf("%q", seen.uri)
	}
}

func TestTransport_ARefusedConnectionIsNetworkWithoutCredentials(t *testing.T) {
	// A port nobody listens on.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	c := liveClient(t, "http://deploy:hunter2@"+addr, withRetries(1))
	_, err = c.GetBalance(ctxBG)
	e := expectError(t, err, "network", 0, true, "")
	if strings.Contains(e.Message, "hunter2") || !strings.HasPrefix(e.Message, "GET /v1/billing/balance: ") {
		t.Errorf("%q", e.Message)
	}
	ctx, cancel := context.WithCancel(ctxBG)
	cancel()
	if _, err := c.GetBalance(ctx); err != context.Canceled {
		t.Errorf("a cancelled context before the first attempt: %v", err)
	}
}
