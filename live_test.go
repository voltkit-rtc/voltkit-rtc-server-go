//go:build live

package voltkit

// The live smoke (spec section 11, item 5) against a scratch controller:
// scripts/test/go-sdk-live.sh starts Redis, a media node and the controller,
// then runs this test with VOLTKIT_LIVE_ORCH_URL and VOLTKIT_LIVE_ADMIN_TOKEN.
// It creates its own project and key through the admin API, runs every call
// in the spec's order, asserting each answer, receives its own webhooks and
// serves the rates stub itself, and deletes the project at the end. Nothing
// it prints is a secret.
//
//	go test -tags live -run '^TestLive$' -v .

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLive(t *testing.T) {
	orch := os.Getenv("VOLTKIT_LIVE_ORCH_URL")
	adminToken := os.Getenv("VOLTKIT_LIVE_ADMIN_TOKEN")
	if orch == "" || adminToken == "" {
		t.Skip("VOLTKIT_LIVE_ORCH_URL and VOLTKIT_LIVE_ADMIN_TOKEN are not set: run scripts/test/go-sdk-live.sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var suffix [4]byte
	_, _ = rand.Read(suffix[:])
	tenant := "go-live-" + hex.EncodeToString(suffix[:])
	admin := adminClient{orch: orch, token: adminToken}

	step := func(name string, run func() error) {
		t.Helper()
		if err := run(); err != nil {
			t.Fatalf("FAIL %s: %v", name, err)
		}
		t.Logf("PASS %s", name)
	}
	check := func(cond bool, format string, args ...any) error {
		if !cond {
			return fmt.Errorf(format, args...)
		}
		return nil
	}
	oneAttempt := func(started time.Time) error {
		// A retried call waits at least 200 ms before its second attempt; a
		// local controller answers in a few milliseconds.
		return check(time.Since(started) < 150*time.Millisecond, "took %s: looks retried", time.Since(started))
	}

	// --- a project and its key --------------------------------------------------
	var apiKey string
	step("admin: create the project", func() error {
		status, body, err := admin.call(ctx, "POST", "/admin/tenants", map[string]any{"tenantId": tenant, "name": "Go SDK live smoke", "plan": "unlimited"})
		if err != nil {
			return err
		}
		return check(status == 201 || status == 200, "status %d: %s", status, body)
	})
	defer func() {
		status, _, err := admin.call(context.Background(), "DELETE", "/admin/tenants/"+tenant, nil)
		if err != nil || status != 200 {
			t.Errorf("cleanup: deleting the project: %d %v", status, err)
		} else {
			t.Logf("PASS cleanup: project deleted")
		}
	}()
	step("admin: issue an API key", func() error {
		status, body, err := admin.call(ctx, "POST", "/admin/tenants/"+tenant+"/keys", map[string]any{"label": "go-live"})
		if err != nil {
			return err
		}
		var reply struct {
			Key struct {
				Secret string `json:"secret"`
			} `json:"key"`
		}
		_ = json.Unmarshal(body, &reply)
		apiKey = reply.Key.Secret
		return check(status == 201 && len(apiKey) > 10, "status %d, key length %d", status, len(apiKey))
	})

	// --- the rates stub: the website API is not in the scratch ------------------
	rates := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/billing/rtc/rates/current" || r.Header.Get("X-API-Key") != "" || r.Header.Get("Authorization") != "" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"version":3,"kind":"standard","effectiveAt":"2026-10-13T00:00:00.000Z","unit":{"price":"credits per 1,000 participant-minutes","rate":"cu per participant-second","cuPerCredit":"6000000"},"tiers":[{"id":"video_30","rank":3,"label":"Video 30","media":"video","credits":"99.00","cuPerParticipantSecond":"9900","base":"99.00","surcharge":null,"available":true}],"surcharges":[],"idleTier":"audio_std","modes":[],"conversion":{"creditsPerRupee":"1.0000","numerator":"1","denominator":"1","inrPerCredit":"1.0000","version":null,"effectiveAt":null,"source":"default"},"notes":[]}}`))
	}))
	defer rates.Close()

	client, err := NewStreamClient(Config{BaseURL: orch, APIKey: apiKey, RatesBaseURL: rates.URL, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("client: %v", client)

	roomID := "go-live-room-" + hex.EncodeToString(suffix[:])
	channel := roomID

	// --- rooms --------------------------------------------------------------------
	step("listRooms: empty", func() error {
		list, err := client.ListRooms(ctx)
		if err != nil {
			return err
		}
		return check(len(list.Rooms) == 0, "%d rooms", len(list.Rooms))
	})
	var hostToken string
	step("ensureRoom: 201, existed false", func() error {
		// The media node registers a moment after it starts; a room needs one.
		deadline := time.Now().Add(45 * time.Second)
		for {
			room, err := client.EnsureRoom(ctx, EnsureRoomParams{RoomID: roomID, HostIdentity: "host-1", Metadata: map[string]any{"seats": 8}})
			var e *Error
			if err != nil && errors.As(err, &e) && e.IsCapacityExhausted() && time.Now().Before(deadline) {
				t.Logf("  waiting for a media node (%s)", e.Code)
				time.Sleep(2 * time.Second)
				continue
			}
			if err != nil {
				return err
			}
			hostToken = room.HostToken
			// The reply need not echo the metadata (the spec marks it optional on
			// the wire); when it does, it is what was sent.
			if len(room.Metadata) > 0 && string(room.Metadata) != `{"seats":8}` {
				return fmt.Errorf("metadata %s", room.Metadata)
			}
			return check(!room.Existed && room.RoomID == roomID && room.HostToken != "" && room.WSURL != "" && room.ExpiresAt > 0,
				"existed %v roomId %q", room.Existed, room.RoomID)
		}
	})
	step("ensureRoom again: 200, existed true, a host token", func() error {
		room, err := client.EnsureRoom(ctx, EnsureRoomParams{RoomID: roomID, HostIdentity: "host-1"})
		if err != nil {
			return err
		}
		// A token minted within the same second as the first has the same
		// claims and so the same bytes: "fresh" means minted now, not different.
		return check(room.Existed && room.HostToken != "" && len(room.HostToken) == len(hostToken), "existed %v, token length %d", room.Existed, len(room.HostToken))
	})
	step("createRoom same id: room-exists 409, not retried", func() error {
		started := time.Now()
		_, err := client.CreateRoom(ctx, CreateRoomParams{RoomID: roomID, HostIdentity: "host-1"})
		var e *Error
		if !errors.As(err, &e) || e.Code != "room-exists" || e.Status != 409 || e.Retryable {
			return fmt.Errorf("%v", err)
		}
		return oneAttempt(started)
	})
	step("createToken: viewer and host", func() error {
		for _, role := range []Role{RoleViewer, RoleHost} {
			tok, err := client.CreateToken(ctx, CreateTokenParams{RoomID: roomID, Identity: "user-" + string(role), Role: role})
			if err != nil {
				return err
			}
			if err := check(tok.Role == role && tok.RoomID == roomID && tok.Token != "" && tok.ExpiresAt > time.Now().Unix(), "%+v", tok); err != nil {
				return err
			}
		}
		return nil
	})
	step("getRoom: 0 participants", func() error {
		view, err := client.GetRoom(ctx, roomID)
		if err != nil {
			return err
		}
		if len(view.Room.Metadata) > 0 && string(view.Room.Metadata) != `{"seats":8}` {
			return fmt.Errorf("metadata %s", view.Room.Metadata)
		}
		t.Logf("  room metadata on the wire: %q", view.Room.Metadata)
		return check(view.Room.RoomID == roomID && view.Room.Participants == 0 && len(view.Participants) == 0, "%+v", view.Room)
	})
	step("listParticipants: []", func() error {
		people, err := client.ListParticipants(ctx, roomID)
		if err != nil {
			return err
		}
		return check(people != nil && len(people) == 0, "%v", people)
	})

	// --- chat -----------------------------------------------------------------------
	step("createChatToken", func() error {
		tok, err := client.CreateChatToken(ctx, CreateChatTokenParams{Channel: channel, Identity: "viewer-9"})
		if err != nil {
			return err
		}
		return check(tok.Role == RoleViewer && tok.Channel == channel && strings.HasSuffix(tok.WSURL, "/ws/chat") && tok.Token != "", "%+v", tok)
	})
	step("getChatInfo: 0 members", func() error {
		info, err := client.GetChatInfo(ctx, channel)
		if err != nil {
			return err
		}
		return check(info.Channel == channel && info.Members == 0, "%+v", info)
	})
	var messageID string
	step("sendMessage: 201", func() error {
		msg, err := client.SendMessage(ctx, SendMessageParams{Channel: channel, Text: "hello from Go", Type: "system", Metadata: map[string]any{"k": 1}})
		if err != nil {
			return err
		}
		messageID = msg.ID
		return check(msg.ID != "" && msg.Text == "hello from Go" && msg.From == "system" && msg.TS > 0, "%+v", msg)
	})
	step("listMessages: holds it, oldest first", func() error {
		if _, err := client.SendMessage(ctx, SendMessageParams{Channel: channel, Text: "second"}); err != nil {
			return err
		}
		msgs, err := client.ListMessages(ctx, channel, &ListMessagesOptions{Limit: 10})
		if err != nil {
			return err
		}
		return check(len(msgs) == 2 && msgs[0].ID == messageID && msgs[1].Text == "second", "%+v", msgs)
	})
	step("deleteMessage", func() error { return client.DeleteMessage(ctx, channel, messageID) })
	step("deleteMessage again: no-message on a first attempt", func() error {
		started := time.Now()
		err := client.DeleteMessage(ctx, channel, messageID)
		var e *Error
		if !errors.As(err, &e) || e.Code != "no-message" || !e.IsNotFound() {
			return fmt.Errorf("%v", err)
		}
		return oneAttempt(started)
	})
	step("muteChat", func() error { return client.MuteChat(ctx, channel, "viewer-9") })
	step("listChatMutes: holds it", func() error {
		muted, err := client.ListChatMutes(ctx, channel)
		if err != nil {
			return err
		}
		return check(len(muted) == 1 && muted[0] == "viewer-9", "%v", muted)
	})
	step("unmuteChat", func() error {
		if err := client.UnmuteChat(ctx, channel, "viewer-9"); err != nil {
			return err
		}
		muted, err := client.ListChatMutes(ctx, channel)
		if err != nil {
			return err
		}
		return check(len(muted) == 0, "%v", muted)
	})

	// --- usage and billing ----------------------------------------------------------
	step("getUsage: roomsCreated 1 today", func() error {
		var usage *UsageSummary
		var err error
		for i := 0; i < 20; i++ {
			usage, err = client.GetUsage(ctx)
			if err != nil {
				return err
			}
			if usage.Today.RoomsCreated >= 1 {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		return check(usage.TenantID == tenant && usage.Today.RoomsCreated == 1 && usage.Plan == "unlimited" && usage.MinutesRemaining == -1, "%+v", usage)
	})
	step("getUsageSeries(7): 7 entries, the last today", func() error {
		series, err := client.GetUsageSeries(ctx, 7)
		if err != nil {
			return err
		}
		today := time.Now().UTC().Format("2006-01-02")
		return check(len(series) == 7 && series[6].Day == today && series[6].RoomsCreated == 1, "%d entries, last %+v", len(series), series[len(series)-1])
	})
	step("getBalance: billing-unavailable on a controller without wallet billing, one attempt", func() error {
		started := time.Now()
		b, err := client.GetBalance(ctx)
		if err == nil {
			t.Logf("  wallet billing is wired on this controller: %+v", b)
			return nil
		}
		var e *Error
		if !errors.As(err, &e) || !e.IsBillingUnavailable() || e.Status != 503 || e.Retryable {
			return fmt.Errorf("%v", err)
		}
		return oneAttempt(started)
	})
	step("listStreams: broadcast-disabled (no OME in the scratch)", func() error {
		streams, err := client.ListStreams(ctx)
		if err == nil {
			t.Logf("  broadcast is configured: %d streams", len(streams))
			return nil
		}
		var e *Error
		if !errors.As(err, &e) || !e.IsBroadcastDisabled() || !e.IsNotFound() {
			return fmt.Errorf("%v", err)
		}
		return nil
	})
	step("a wrong key: unauthorized, one attempt", func() error {
		wrong, err := NewStreamClient(Config{BaseURL: orch, APIKey: "sk_" + strings.Repeat("0", 64)})
		if err != nil {
			return err
		}
		started := time.Now()
		_, err = wrong.GetUsage(ctx)
		var e *Error
		if !errors.As(err, &e) || e.Code != "unauthorized" || e.Status != 401 || e.Retryable {
			return fmt.Errorf("%v", err)
		}
		return oneAttempt(started)
	})

	// --- the room ends --------------------------------------------------------------
	step("deleteRoom", func() error { return client.DeleteRoom(ctx, roomID) })
	step("getRoom: no-room", func() error {
		_, err := client.GetRoom(ctx, roomID)
		var e *Error
		if !errors.As(err, &e) || e.Code != "no-room" || !e.IsNotFound() {
			return fmt.Errorf("%v", err)
		}
		return nil
	})
	step("deleteRoom again: no-room (a first-attempt 404 fails)", func() error {
		err := client.DeleteRoom(ctx, roomID)
		var e *Error
		if !errors.As(err, &e) || e.Code != "no-room" {
			return fmt.Errorf("%v", err)
		}
		return nil
	})

	// --- webhooks -------------------------------------------------------------------
	receiver := newLiveReceiver()
	defer receiver.Close()
	hook := "/admin/tenants/" + tenant + "/webhook"
	var first, second string
	step("webhook: PUT the endpoint, a secret is issued", func() error {
		status, body, err := admin.call(ctx, "PUT", hook, map[string]any{"url": receiver.URL + "/hook", "allEvents": true})
		if err != nil {
			return err
		}
		var reply struct {
			Secret  string `json:"secret"`
			Created bool   `json:"created"`
		}
		_ = json.Unmarshal(body, &reply)
		first = reply.Secret
		receiver.setSecrets(first)
		return check(status == 200 && reply.Created && strings.HasPrefix(first, "whsec_") && len(first) == 70, "status %d created %v secret length %d", status, reply.Created, len(first))
	})
	step("webhook: a test event is delivered and verifies", func() error {
		status, _, err := admin.call(ctx, "POST", hook+"/test", nil)
		if err != nil {
			return err
		}
		if status != 202 {
			return fmt.Errorf("test: %d", status)
		}
		d, err := receiver.wait(ctx, 1)
		if err != nil {
			return err
		}
		return check(d.event != nil && d.event.Type == "webhook.test" && d.event.TenantID == tenant && d.event.RoomID == "" && d.headers.Get("User-Agent") == "VoltKit-Webhooks/1" && d.headers.Get("X-Stream-Delivery") == d.event.ID,
			"verified %v type %q headers %v", d.event != nil, d.eventType(), d.headers)
	})
	step("webhook: rotate with an overlap", func() error {
		status, body, err := admin.call(ctx, "POST", hook+"/rotate", map[string]any{"overlapSeconds": 3600})
		if err != nil {
			return err
		}
		var reply struct {
			Secret string `json:"secret"`
		}
		_ = json.Unmarshal(body, &reply)
		second = reply.Secret
		return check(status == 200 && strings.HasPrefix(second, "whsec_") && second != first, "status %d", status)
	})
	step("webhook: a delivery during the overlap carries two signatures; the old secret, the new one and both verify", func() error {
		receiver.setSecrets(first) // this receiver has not been told the new secret yet
		if status, _, err := admin.call(ctx, "POST", hook+"/test", nil); err != nil || status != 202 {
			return fmt.Errorf("test: %d %v", status, err)
		}
		d, err := receiver.wait(ctx, 2)
		if err != nil {
			return err
		}
		header := d.headers.Get("X-Stream-Signature")
		if err := check(strings.Count(header, "v1=") == 2, "signatures in %q", header); err != nil {
			return err
		}
		if err := check(d.event != nil, "a receiver on the old secret still verifies"); err != nil {
			return err
		}
		for name, secrets := range map[string][]string{"the old secret": {first}, "the new secret": {second}, "both": {first, second}, "both, the other way round": {second, first}} {
			if !VerifyWebhook(d.body, header, secrets, nil) {
				return fmt.Errorf("%s does not verify the delivery", name)
			}
		}
		if VerifyWebhook(d.body, header, []string{"whsec_" + strings.Repeat("0", 64)}, nil) {
			return errors.New("an unrelated secret verified the delivery")
		}
		if _, err := ParseWebhook(d.body, header, []string{second}, nil); err != nil {
			return fmt.Errorf("ParseWebhook with the new secret: %v", err)
		}
		return nil
	})

	// --- rates ------------------------------------------------------------------------
	step("getRates: the envelope from a local stub, no credential sent", func() error {
		r, err := client.GetRates(ctx)
		if err != nil {
			return err
		}
		return check(r.Version == 3 && r.Kind == "standard" && len(r.Tiers) == 1 && r.Tiers[0].CuPerParticipantSecond == "9900" && r.Tiers[0].Surcharge == nil && r.Conversion.Version == nil, "%+v", r)
	})
}

// adminClient talks to the controller's admin API with the admin token.
type adminClient struct {
	orch, token string
}

func (a adminClient) call(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.orch+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("X-API-Key", a.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	text, err := io.ReadAll(resp.Body)
	return resp.StatusCode, text, err
}

// liveReceiver is the webhook endpoint: it verifies every delivery with the
// SDK against the secrets it currently holds and records what arrived.
type liveReceiver struct {
	*httptest.Server
	mu         sync.Mutex
	secrets    []string
	deliveries []liveDelivery
}

type liveDelivery struct {
	headers http.Header
	body    []byte
	event   *StreamEvent
}

func (d liveDelivery) eventType() string {
	if d.event == nil {
		return ""
	}
	return d.event.Type
}

func newLiveReceiver() *liveReceiver {
	r := &liveReceiver{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		secrets := r.secrets
		r.mu.Unlock()
		event, err := ParseWebhook(body, req.Header.Get("X-Stream-Signature"), secrets, nil)
		if err != nil {
			event = nil
		}
		r.mu.Lock()
		r.deliveries = append(r.deliveries, liveDelivery{headers: req.Header.Clone(), body: body, event: event})
		r.mu.Unlock()
		if event == nil {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(200)
	}))
	return r
}

func (r *liveReceiver) setSecrets(secrets ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.secrets = secrets
}

// wait returns the n-th delivery (1-based) once it has arrived.
func (r *liveReceiver) wait(ctx context.Context, n int) (liveDelivery, error) {
	deadline := time.Now().Add(20 * time.Second)
	for {
		r.mu.Lock()
		if len(r.deliveries) >= n {
			d := r.deliveries[n-1]
			r.mu.Unlock()
			return d, nil
		}
		r.mu.Unlock()
		if time.Now().After(deadline) || ctx.Err() != nil {
			return liveDelivery{}, fmt.Errorf("delivery %d never arrived", n)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
