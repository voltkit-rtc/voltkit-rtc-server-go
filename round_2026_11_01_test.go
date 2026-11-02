package voltkit

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The testers' round of 2026-11-01 (G013-1, G013-2) and the lead's decision D11: a decode error never quotes the
// reply's or the body's text. encoding/json quotes a mismatched number literal whole in its error, so a reply within
// the 8 MiB cap gave an Error() of up to 8 MiB; and from Go 1.25 on (encoding/json on its v2 implementation) the
// decoder's field path holds map keys, list indexes and the reply's own spelling of a case-folded key. Every test
// here failed on 0.1.3.

// shortMessage fails the test when msg is 300 bytes or longer, with a cut
// preview (never the whole message: on 0.1.3 it was the size of the number).
func shortMessage(t testing.TB, msg string) {
	t.Helper()
	if len(msg) >= 300 {
		t.Fatalf("message is %d bytes: %.160s...", len(msg), msg)
	}
}

// noReplyText fails the test when msg repeats any of replyOnly: the digits of
// a number, a key or a spelling that only the reply has.
func noReplyText(t testing.TB, msg string, replyOnly ...string) {
	t.Helper()
	for _, s := range replyOnly {
		if strings.Contains(msg, s) {
			t.Errorf("message repeats %q from the reply: %.200s", s, msg)
		}
	}
}

func TestDecode_AHugeNumberLiteralGivesAShortErrorWithoutTheReply(t *testing.T) {
	// G013-1. A 1 MiB number in a numeric field: within the reply cap, so it is read; the decoder's error quotes it whole.
	big := strings.Repeat("9", 1<<20)
	body := `{"channel":"g1","members":` + big + `}`
	ft := scripted(rawStep(200, body))
	_, err := newClient(t, ft).GetChatInfo(ctxBG, "g1")
	e := asError(t, err)
	shortMessage(t, e.Message)
	shortMessage(t, err.Error())
	expectError(t, err, "http-200", 200, false,
		fmt.Sprintf(`GET /v1/chat/g1: the reply could not be decoded: "members": JSON type number, where this SDK reads int (byte %d of the reply)`, len(body)-1))
	noReplyText(t, e.Message, "9999")
	if ft.count() != 1 {
		t.Errorf("%d attempts, want 1", ft.count())
	}
	// Control: the same reply with a number that fits decodes.
	info, err := newClient(t, scripted(rawStep(200, `{"channel":"g1","members":3}`))).GetChatInfo(ctxBG, "g1")
	if err != nil || info.Members != 3 {
		t.Errorf("control: %+v, %v", info, err)
	}
}

func TestDecode_AMapKeyFromTheReplyNeverReachesTheMessage(t *testing.T) {
	// The path stops at a map (its keys are the reply's) and keeps the SDK's own spelling of a case-folded key.
	for _, tc := range []struct {
		body, want string
		replyOnly  []string
	}{
		{`{"wallet":{"scopes":{"customer-note-from-the-reply":5}}}`,
			`"wallet.scopes": JSON type number, where this SDK reads voltkit.WalletScopeAmount (byte 53 of the reply)`,
			[]string{"customer-note", "customer"}},
		{`{"wallet":{"scopes":{"k.with.dots":{"paidCu":5}}}}`,
			`"wallet.scopes": JSON type number, where this SDK reads string (byte 46 of the reply)`,
			[]string{"k.with", "dots", "paidCu"}},
		{`{"WALLET":{"SPENDORDER":"x"}}`,
			`"wallet.spendOrder": JSON type string, where this SDK reads []string (byte 27 of the reply)`,
			[]string{"WALLET", "SPENDORDER", `"x"`}},
	} {
		_, err := newClient(t, scripted(rawStep(200, tc.body))).GetBalance(ctxBG)
		e := asError(t, err)
		shortMessage(t, e.Message)
		expectError(t, err, "http-200", 200, false, "GET /v1/billing/balance: the reply could not be decoded: "+tc.want)
		noReplyText(t, e.Message, tc.replyOnly...)
	}
}

func TestDecode_ListIndexesAndEmbeddedStructsResolveToTheFieldName(t *testing.T) {
	// Go 1.25+ writes list indexes into the path ("rooms.1.participants"), older decoders the embedded struct's
	// Go name ("series.Usage.roomSeconds"); the SDK names the field either way.
	rooms := `{"rooms":[{"roomId":"a"},{"roomId":"b","participants":"many"}]}`
	_, err := newClient(t, scripted(rawStep(200, rooms))).ListRooms(ctxBG)
	expectError(t, err, "http-200", 200, false, fmt.Sprintf(
		`GET /v1/rooms: the reply could not be decoded: "rooms.participants": JSON type string, where this SDK reads int (byte %d of the reply)`,
		strings.Index(rooms, `"many"`)+6))
	noReplyText(t, asError(t, err).Message, "many", ".1.", "Room.")

	series := `{"series":[{"day":"2026-11-01","roomSeconds":1},{"day":"2026-11-02","roomSeconds":"an hour"}]}`
	_, err = newClient(t, scripted(rawStep(200, series))).GetUsageSeries(ctxBG, 2)
	expectError(t, err, "http-200", 200, false, fmt.Sprintf(
		`GET /v1/usage/series?days=2: the reply could not be decoded: "series.roomSeconds": JSON type string, where this SDK reads int64 (byte %d of the reply)`,
		strings.Index(series, `"an hour"`)+9))
	noReplyText(t, asError(t, err).Message, "an hour", ".1.", "Usage.")

	// RoomWithHostToken embeds Room: a wrong type in a promoted field of CreateRoom's reply.
	room := `{"roomId":"r","createdAt":"yesterday"}`
	_, err = newClient(t, scripted(rawStep(200, room))).CreateRoom(ctxBG, CreateRoomParams{RoomID: "r", HostIdentity: "h"})
	expectError(t, err, "http-200", 200, false, fmt.Sprintf(
		`POST /v1/rooms: the reply could not be decoded: "createdAt": JSON type string, where this SDK reads int64 (byte %d of the reply)`, len(room)-1))
	noReplyText(t, asError(t, err).Message, "yesterday", "Room.")
}

func TestDecode_TheRatesDecodeUsesTheSameWords(t *testing.T) {
	// methods.go's own decode of the envelope's data object: the offset counts from that object.
	body := `{"success":true,"data":{"version":"four"}}`
	_, err := newClient(t, scripted(rawStep(200, body)), withRates("https://api.example.com")).GetRates(ctxBG)
	expectError(t, err, "http-200", 200, false,
		`GET /api/v1/billing/rtc/rates/current: the reply could not be decoded: "version": JSON type string, where this SDK reads int (byte 17 of the data object)`)
	noReplyText(t, asError(t, err).Message, "four")
}

func TestWebhooks_ParseErrorQuotesNothingOfTheBody(t *testing.T) {
	// G013-2: ParseWebhook wrapped the decoder's error with %w, so a validly signed body holding a 1 MiB number gave an
	// error of that size. The controller never sends one; the test signs it itself.
	big := strings.Repeat("9", 1<<20)
	body := `{"id":"evt_1","type":"webhook.test","tenantId":"vk","createdAt":` + big + `}`
	_, err := ParseWebhook([]byte(body), signedHeader(body, "s", goldenTS), []string{"s"}, at(goldenTS))
	if err == nil {
		t.Fatal("no error")
	}
	shortMessage(t, err.Error())
	want := fmt.Sprintf(`voltkit: the webhook body is not JSON: "createdAt": JSON type number, where this SDK reads int64 (byte %d of the body)`, len(body)-1)
	if err.Error() != want {
		t.Errorf("\n got %q\nwant %q", err.Error(), want)
	}
	var e *Error
	if errors.As(err, &e) {
		t.Errorf("a plain error, not *Error: %v", err)
	}
	noReplyText(t, err.Error(), "9999", "evt_1", "webhook.test")
	// A signed body that is not JSON at all: the byte where it breaks, not the character found there.
	_, err = ParseWebhook([]byte("not json"), signedHeader("not json", "s", goldenTS), []string{"s"}, at(goldenTS))
	if err == nil || !strings.HasPrefix(err.Error(), "voltkit: the webhook body is not JSON: ") || strings.Contains(err.Error(), "'") {
		t.Errorf("%v", err)
	}
	// Control: a signed good event still parses, the golden vector byte for byte.
	ev, err := ParseWebhook([]byte(goldenBody), "t=1787000000,v1="+goldenCurrent, []string{"whsec_golden_current"}, at(goldenTS))
	if err != nil || ev.ID != "evt_golden" || ev.CreatedAt != 1787000000000 {
		t.Errorf("control: %+v, %v", ev, err)
	}
}

func TestDecode_ModelFieldWalksTheSDKsTypesOnly(t *testing.T) {
	// Both decoders' path styles, whichever Go runs the suite: Go 1.25+ (map keys, list indexes, the reply's own
	// spelling of a case-folded key) and Go 1.24 and before (embedded struct names, no indexes).
	type series struct {
		Series []DailyUsage `json:"series"`
	}
	for _, tc := range []struct {
		out  any
		path string
		want string
	}{
		{&ProjectBalance{}, "wallet.scopes.customer-note-from-the-reply", "wallet.scopes"},
		{&ProjectBalance{}, "wallet.scopes.k.with.dots.paidCu", "wallet.scopes"},
		{&ProjectBalance{}, "wallet.scopes", "wallet.scopes"},
		{&ProjectBalance{}, "WALLET.SPENDORDER", "wallet.spendOrder"},
		{&ProjectBalance{}, "wallet.count-from-the-reply", "wallet"},
		{&ProjectBalance{}, "nothing-like-this.x", ""},
		{&ProjectBalance{}, "", ""},
		{&RoomList{}, "rooms.1.participants", "rooms.participants"},
		{&RoomList{}, "rooms.participants", "rooms.participants"},
		{&RoomList{}, "rooms.1", "rooms"},
		{&series{}, "series.0.roomSeconds", "series.roomSeconds"},
		{&series{}, "series.Usage.roomSeconds", "series.roomSeconds"},
		{&series{}, "series.Usage.ROOMSECONDS", "series.roomSeconds"},
		{&RoomWithHostToken{}, "Room.createdAt", "createdAt"},
		{&RoomWithHostToken{}, "createdAt", "createdAt"},
		{&RoomWithHostToken{}, "hostToken", "hostToken"},
		{&BroadcastStreamWithPublish{}, "BroadcastStream.streamId", "streamId"},
		{&BroadcastStreamWithPublish{}, "streamId", "streamId"},
		{&StreamEvent{}, "createdAt", "createdAt"},
		{&StreamEvent{}, "data.anything-from-the-body", "data"},
		{nil, "x", ""},
	} {
		if got := modelField(reflect.TypeOf(tc.out), tc.path); got != tc.want {
			t.Errorf("%T %q: got %q, want %q", tc.out, tc.path, got, tc.want)
		}
	}
}
