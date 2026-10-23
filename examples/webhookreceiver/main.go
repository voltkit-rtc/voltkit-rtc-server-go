// Command webhookreceiver receives VoltKit RTC webhooks on net/http: it reads
// the raw body, verifies the signature with ParseWebhook against one or more
// secrets (so a rotation drops no delivery), answers 401 when a delivery does
// not verify, answers 200 before doing any work, and deduplicates on the event
// id, because delivery is at least once.
//
//	VOLTKIT_RTC_WEBHOOK_SECRETS=whsec_current,whsec_previous go run .
//
// Point the webhook at http://<this host>:8098/hooks/voltkit from the VoltKit
// dashboard. During a rotation, list the new secret and the old one, comma
// separated; after the overlap, drop the old one.
package main

import (
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	voltkit "github.com/voltkit-rtc/voltkit-rtc-server-go"
)

func main() {
	var secrets []string
	for _, s := range strings.Split(os.Getenv("VOLTKIT_RTC_WEBHOOK_SECRETS"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			secrets = append(secrets, s)
		}
	}
	if len(secrets) == 0 {
		log.Fatal("VOLTKIT_RTC_WEBHOOK_SECRETS is not set: the signing secret from the dashboard, and the previous one during a rotation, comma separated")
	}

	work := make(chan *voltkit.StreamEvent, 1024)
	go worker(work)
	seen := newDedupe(24 * time.Hour)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /hooks/voltkit", func(w http.ResponseWriter, r *http.Request) {
		// The signature covers the exact bytes sent: read them raw, never
		// through a JSON decoder and back.
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		event, err := voltkit.ParseWebhook(body, r.Header.Get("X-Stream-Signature"), secrets, nil)
		if err != nil {
			var e *voltkit.Error
			if errors.As(err, &e) {
				// bad-signature: not from VoltKit, or too old to trust. VoltKit
				// retries a 401, so a clock that is off shows up in its log.
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			// A plain error is a mistake in this receiver's setup.
			log.Printf("webhook: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK) // answer first: a slow handler is retried as a duplicate

		if !seen.first(event.ID) {
			return // delivered before: at-least-once delivery, by design
		}
		select {
		case work <- event:
		default:
			log.Printf("webhook: queue full, dropping %s %s", event.Type, event.ID)
		}
	})

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = "127.0.0.1:8098"
	}
	log.Printf("webhook receiver on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// worker does the work after the reply has gone out. Events are not ordered:
// a participant.left can arrive before the participant.joined it followed.
func worker(events <-chan *voltkit.StreamEvent) {
	for ev := range events {
		log.Printf("event %s %s room=%q data=%s", ev.Type, ev.ID, ev.RoomID, ev.Data)
	}
}

// dedupe remembers event ids for a while. In production, use your database or
// cache so every instance of the receiver shares the memory.
type dedupe struct {
	mu   sync.Mutex
	ttl  time.Duration
	seen map[string]time.Time
}

func newDedupe(ttl time.Duration) *dedupe {
	return &dedupe{ttl: ttl, seen: map[string]time.Time{}}
}

// first reports whether id has not been seen before, and remembers it.
func (d *dedupe) first(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	for k, t := range d.seen {
		if now.Sub(t) > d.ttl {
			delete(d.seen, k)
		}
	}
	if _, dup := d.seen[id]; dup {
		return false
	}
	d.seen[id] = now
	return true
}
