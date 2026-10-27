// Command tokenserver is a minimal token endpoint on net/http: a client asks
// to join a room, this server decides the role and mints a short-lived room
// token with the VoltKit RTC server SDK. The API secret never leaves this
// process.
//
//	VOLTKIT_RTC_URL=https://api.voltkit.in VOLTKIT_RTC_SECRET=sk_... go run .
//	curl -X POST -H 'X-Demo-User: user-7' http://127.0.0.1:8099/rooms/show-42/join
//
// Replace decideRole and the X-Demo-User header with your own session and
// authorization: who the caller is, and what they may do, is your backend's
// decision, and it is signed into the token so the client cannot change it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	voltkit "github.com/voltkit-rtc/voltkit-rtc-server-go"
)

func main() {
	// Reads VOLTKIT_RTC_URL and VOLTKIT_RTC_SECRET; checks them at startup so
	// a missing or malformed value fails here, not on a user's first request.
	client, err := voltkit.NewStreamClientFromEnv()
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /rooms/{roomId}/join", join(client))

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = "127.0.0.1:8099"
	}
	log.Printf("token server on %s (client %v)", addr, client) // the client prints without its secret
	log.Fatal(http.ListenAndServe(addr, mux))
}

// decideRole is the placeholder for your rules: the show's owner is the host,
// invited co-hosts are cohosts, your moderators moderate, everyone else watches.
func decideRole(roomID, identity string) voltkit.Role {
	_ = roomID
	if identity == "user-7" {
		return voltkit.RoleHost
	}
	return voltkit.RoleViewer
}

func join(client *voltkit.StreamClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Who is asking: your session middleware knows. The demo trusts a header.
		identity := r.Header.Get("X-Demo-User")
		if identity == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in first"})
			return
		}
		roomID := r.PathValue("roomId")

		// A request a user is waiting on: a shorter budget than the SDK's
		// default (10 s per attempt, 3 attempts) is kinder. The context
		// cancels the SDK's retries when the client goes away.
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		tok, err := client.CreateToken(ctx, voltkit.CreateTokenParams{
			RoomID:   roomID,
			Identity: identity,
			Role:     decideRole(roomID, identity),
		})
		if err != nil {
			writeError(w, err)
			return
		}
		if tok.Token == "" || tok.WSURL == "" {
			// A 200 without a usable token is no success. Logged by room, never the reply: it may hold a token.
			log.Printf("join: CreateToken returned no token or wsUrl for room %q", roomID)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the RTC service returned an incomplete token; try again"})
			return
		}
		w.Header().Set("Cache-Control", "no-store") // a token is a credential for one room
		writeJSON(w, http.StatusOK, map[string]any{
			"token":     tok.Token,
			"wsUrl":     tok.WSURL,
			"role":      tok.Role,
			"expiresAt": tok.ExpiresAt,
		})
	}
}

// writeError maps a failed call to an answer for the client, by code: the
// room is gone (404), the account is out of minutes or credits (402), the
// SDK's retries are spent and later may work (503 with Retry-After), and
// everything else (a wrong key, a suspended account, a bad call) is this
// server's problem, answered as a 500 that gives nothing away.
func writeError(w http.ResponseWriter, err error) {
	var e *voltkit.Error
	if !errors.As(err, &e) {
		// Not an answer from VoltKit: the request was cancelled, or this
		// handler has a bug.
		log.Printf("join: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "something went wrong"})
		return
	}
	log.Printf("join: %v", e) // safe to log whole: the API secret is never in it
	switch {
	case e.IsNotFound(): // no-room
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this show has ended"})
	case e.IsOutOfMinutes(): // quota-exhausted, daily-limit-reached, monthly-limit-reached
		writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": "this show cannot take viewers right now"})
	case e.Code == "viewer-quota":
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "this show is full"})
	case e.Retryable: // network, 429, 5xx: the SDK has already retried
		w.Header().Set("Retry-After", "10")
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "try again in a moment"})
	default: // unauthorized, tenant-suspended, bad-request, ...: fix the server, tell the user nothing
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "something went wrong"})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
