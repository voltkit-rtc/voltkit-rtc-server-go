package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	voltkit "github.com/voltkit-rtc/voltkit-rtc-server-go"
)

// controller is a client whose every call VoltKit answers with status and body.
func controller(t *testing.T, status int, body string) *voltkit.StreamClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	client, err := voltkit.NewStreamClient(voltkit.Config{BaseURL: srv.URL, APIKey: "sk_test_fake"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// askForToken is POST /rooms/show-42/join as user-7; it returns the answer and what was logged.
func askForToken(t *testing.T, client *voltkit.StreamClient) (*httptest.ResponseRecorder, string) {
	t.Helper()
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	req := httptest.NewRequest(http.MethodPost, "/rooms/show-42/join", nil)
	req.SetPathValue("roomId", "show-42")
	req.Header.Set("X-Demo-User", "user-7")
	rec := httptest.NewRecorder()
	join(client)(rec, req)
	return rec, logged.String()
}

func TestJoinAnswers502ForAnIncompleteToken(t *testing.T) {
	replies := map[string]string{
		"empty object":  `{}`,
		"empty strings": `{"token":"","wsUrl":"","expiresAt":1787000600}`,
		"no wsUrl":      `{"token":"tok_never_logged","expiresAt":1787000600}`,
		"no token":      `{"wsUrl":"wss://x.test/ws","expiresAt":1787000600}`,
		"null token":    `{"token":null,"wsUrl":"wss://x.test/ws","expiresAt":1787000600}`,
	}
	for name, reply := range replies {
		t.Run(name, func(t *testing.T) {
			rec, logged := askForToken(t, controller(t, http.StatusOK, reply))
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status %d, want 502; body %s", rec.Code, rec.Body)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] != "the RTC service returned an incomplete token; try again" {
				t.Fatalf("body %s (%v)", rec.Body, err)
			}
			if !strings.Contains(logged, "show-42") || strings.Contains(logged, "tok_never_logged") {
				t.Fatalf("the log names the room, never the reply: %q", logged)
			}
		})
	}
}

func TestJoinHandsOnACompleteToken(t *testing.T) {
	client := controller(t, http.StatusOK, `{"token":"tok","wsUrl":"wss://x.test/ws","role":"host","identity":"user-7","roomId":"show-42","expiresAt":1787000600}`)
	rec, _ := askForToken(t, client)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["token"] != "tok" || body["wsUrl"] != "wss://x.test/ws" {
		t.Fatalf("body %s (%v)", rec.Body, err)
	}
}
