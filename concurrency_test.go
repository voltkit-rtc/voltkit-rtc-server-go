package voltkit

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// One client, 16 goroutines, successes and redacted errors at once (spec
// section 9: a client is safe to share). Run under -race.
func TestConcurrency_OneClientSixteenGoroutines(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		switch r.URL.Path {
		case "/v1/rooms":
			_, _ = w.Write([]byte(`{"rooms":[{"roomId":"r"}]}`))
		case "/v1/billing/balance":
			w.WriteHeader(401)
			_, _ = fmt.Fprintf(w, `{"error":"unauthorized","message":"bad key %s and %s"}`, testKey, "sk_test_0123456789abcdef0123456789abcde%66")
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no-room","message":"not found"}`))
		}
	})
	c := liveClient(t, srv.URL)
	var wg sync.WaitGroup
	errs := make(chan string, 16*30)
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				list, err := c.ListRooms(ctxBG)
				if err != nil || len(list.Rooms) != 1 {
					errs <- fmt.Sprintf("goroutine %d: ListRooms %v %v", g, list, err)
				}
				// errors.As rather than the asError helper: t.Fatalf must not be
				// called from a goroutine other than the test's.
				_, err = c.GetBalance(ctxBG)
				var e *Error
				if !errors.As(err, &e) || e.Code != "unauthorized" || e.Message != "GET /v1/billing/balance: bad key [redacted] and [redacted]" {
					errs <- fmt.Sprintf("goroutine %d: GetBalance %v", g, err)
				}
				_, err = c.GetRoom(ctxBG, fmt.Sprintf("room-%d-%d", g, i))
				if !errors.As(err, &e) || !e.IsNotFound() || !strings.HasSuffix(e.Message, ": not found") {
					errs <- fmt.Sprintf("goroutine %d: GetRoom %v", g, err)
				}
				_ = fmt.Sprint(c)
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
	if srv.hits.Load() != 16*30 {
		t.Errorf("%d hits, want %d", srv.hits.Load(), 16*30)
	}
}
