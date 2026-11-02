package voltkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Regression check of the SDK round of 2026-11-01 (D11, Go v0.1.4), by the lead's verifier. decodeProblem replaced
// the decoder's words in every decode error of the SDK. What must not have moved: a valid reply or event decodes
// as before (the whole suite and the shared vectors cover the calls; the event is compared with a plain
// json.Unmarshal here), the *Error code and status of a decode failure, and the error's shape for ParseWebhook,
// where the changelog says errors.As for *json.SyntaxError stops matching: pinned, so a later change is a decision.
// And decodeProblem runs inside the error path of every call, so it must never panic, whatever json.Unmarshal
// hands it, and must never repeat the reply's text.

const verify1101Secret = "whsec_verify_1101"

func verify1101Opts() *VerifyOptions {
	return &VerifyOptions{Now: func() time.Time { return time.Unix(1787000000, 0) }}
}

func TestVerify1101_AValidEventStillDecodesAsAPlainUnmarshalDoes(t *testing.T) {
	body := `{"id":"evt_1","type":"chat.message","tenantId":"t","roomId":"r","createdAt":1787000000123,` +
		`"data":{"text":"é😀\n","score":12.5,"n":1e3,"big":12345678901234567890,"nested":{"a":[1,{"b":[]}]}},"unknown":true}`
	event, err := ParseWebhook([]byte(body), signedHeader(body, verify1101Secret, 1787000000), []string{verify1101Secret}, verify1101Opts())
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	var plain StreamEvent
	if err := json.Unmarshal([]byte(body), &plain); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*event, plain) {
		t.Errorf("event differs from a plain Unmarshal:\n got %+v\nwant %+v", *event, plain)
	}
	if event.CreatedAt != 1787000000123 || string(event.Data) == "" {
		t.Errorf("createdAt %d, data %q", event.CreatedAt, event.Data)
	}
}

func TestVerify1101_ParseWebhookErrorsNoLongerWrapTheDecoderAsTheChangelogSays(t *testing.T) {
	cases := []struct {
		name, body, replyOnly string
		wantInMessage         string
	}{
		{"a syntax error", `{"id":"e1","type":"x"`, "", "the JSON breaks at byte"},
		{"a type mismatch", `{"id":"e1","type":"room.ended","tenantId":"t","createdAt":"SECRET-NOT-A-NUMBER"}`, "SECRET-NOT-A-NUMBER",
			`"createdAt": JSON type string, where this SDK reads int64 (byte`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseWebhook([]byte(tc.body), signedHeader(tc.body, verify1101Secret, 1787000000), []string{verify1101Secret}, verify1101Opts())
			if err == nil {
				t.Fatal("decoded")
			}
			var syntax *json.SyntaxError
			var mismatch *json.UnmarshalTypeError
			var sdk *Error
			if errors.As(err, &syntax) || errors.As(err, &mismatch) {
				t.Errorf("0.1.4 is documented to wrap nothing, yet errors.As found the decoder's error in %v", err)
			}
			if errors.As(err, &sdk) {
				t.Errorf("a not-JSON body is a plain error, never an *Error: %v", err)
			}
			if errors.Unwrap(err) != nil {
				t.Errorf("wraps %v", errors.Unwrap(err))
			}
			if !strings.HasPrefix(err.Error(), "voltkit: the webhook body is not JSON: ") || !strings.Contains(err.Error(), tc.wantInMessage) {
				t.Errorf("message: %q", err.Error())
			}
			if tc.replyOnly != "" && strings.Contains(err.Error(), tc.replyOnly) {
				t.Errorf("the message repeats the body's text: %q", err.Error())
			}
		})
	}
}

func TestVerify1101_ADecodeFailureKeepsItsCodeAndStatusAndNamesTheSdksField(t *testing.T) {
	// GetRoom with a string where int64 is read: http-200, status 200, not retryable, one attempt, the SDK's own
	// field path, and nothing of the reply's text (0.1.3 quoted the decoder's words, which held the literal).
	body := `{"room":{"roomId":"r","tenantId":"t","originNode":"n1","state":"live","hostIdentity":"h","createdAt":"SECRET-1234","nodes":[],"participants":0}}`
	ft := scripted(rawStep(200, body))
	_, err := newClient(t, ft).GetRoom(ctxBG, "r")
	e := asError(t, err)
	if e.Code != "http-200" || e.Status != 200 || e.Retryable {
		t.Errorf("code %s status %d retryable %v", e.Code, e.Status, e.Retryable)
	}
	if ft.count() != 1 {
		t.Errorf("%d attempts, want 1", ft.count())
	}
	want := `GET /v1/rooms/r: the reply could not be decoded: "room.createdAt": JSON type string, where this SDK reads int64 (byte `
	if !strings.HasPrefix(e.Message, want) || strings.Contains(e.Message, "SECRET") {
		t.Errorf("message: %q", e.Message)
	}
	// Control: the same reply with a whole number decodes, and a 1e3 or 12.0 for an int64 is refused as it was on
	// 0.1.3 (encoding/json never read a float into an int64; nothing moved there).
	for _, lit := range []string{"1787000000000", "0", "-1"} {
		room, err := newClient(t, scripted(rawStep(200, strings.Replace(body, `"SECRET-1234"`, lit, 1)))).GetRoom(ctxBG, "r")
		if err != nil || fmt.Sprint(room.Room.CreatedAt) != lit {
			t.Errorf("createdAt %s: %+v, %v", lit, room, err)
		}
	}
	for _, lit := range []string{"12.0", "12.5", "1e3"} {
		_, err := newClient(t, scripted(rawStep(200, strings.Replace(body, `"SECRET-1234"`, lit, 1)))).GetRoom(ctxBG, "r")
		if e := asError(t, err); e.Code != "http-200" || !strings.Contains(e.Message, `"room.createdAt": JSON type number, where this SDK reads int64`) {
			t.Errorf("createdAt %s: %v", lit, err)
		}
	}
}

// quotingUnmarshaler's own error repeats the text it was given, as a third-party type's might.
type quotingUnmarshaler struct{}

func (*quotingUnmarshaler) UnmarshalJSON(b []byte) error { return fmt.Errorf("cannot read %s", b) }

func TestVerify1101_DecodeProblemNeverPanicsAndNeverRepeatsTheReply(t *testing.T) {
	type embedded struct {
		Inner int `json:"inner"`
	}
	type withEmbedded struct {
		embedded
		Named *embedded `json:"named"`
		Skip  int       `json:"-"`
		List  []Room    `json:"list"`
		M     map[string]Room
		Ptr   **Room `json:"ptr"`
	}
	var nilRoom *Room
	outs := []any{nil, &Room{}, Room{}, &nilRoom, &[]Room{}, &map[string]any{}, new(any), &withEmbedded{}, &[]*withEmbedded{}, &struct{}{}, 42, "s"}
	fields := []string{"", ".", "..", "a..b", "0", "0.roomId", "room.createdAt", "metadata.k.deep", "M.key.roomId", "list.3.nodes.0",
		"embedded.inner", "inner", "named.inner", "Skip", "ptr.roomId", "ROOMID", "nodes.0.x", "x.y.z", strings.Repeat("a.", 100)}
	errs := []error{
		&json.UnmarshalTypeError{Value: "number SECRET-99", Offset: 3},
		&json.UnmarshalTypeError{Value: "string", Type: reflect.TypeOf(int64(0)), Offset: 7},
		&json.UnmarshalTypeError{Value: "", Type: reflect.TypeOf(Room{}), Offset: -1},
		&json.SyntaxError{Offset: 5},
		&json.InvalidUnmarshalError{},
		&json.InvalidUnmarshalError{Type: reflect.TypeOf(Room{})},
		errors.New("a custom unmarshaler said: SECRET-TEXT"),
		fmt.Errorf("wrapped: %w", &json.UnmarshalTypeError{Value: "object", Type: reflect.TypeOf(""), Field: "room.state", Offset: 9}),
	}
	for _, out := range outs {
		for _, field := range fields {
			for _, base := range errs {
				err := base
				if m, ok := base.(*json.UnmarshalTypeError); ok {
					copyOf := *m
					copyOf.Field = field
					err = &copyOf
				}
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Fatalf("panic for out %T, field %q, err %T: %v", out, field, err, r)
						}
					}()
					msg := decodeProblem(err, out, "reply")
					if msg == "" || strings.Contains(msg, "SECRET") {
						t.Errorf("out %T, field %q, err %T: %q", out, field, err, msg)
					}
				}()
			}
		}
	}
	// A real decode whose unmarshaler quotes the text: the kind of error, nothing more.
	var target struct {
		A quotingUnmarshaler `json:"a"`
	}
	err := json.Unmarshal([]byte(`{"a":"SECRET-TEXT"}`), &target)
	if err == nil {
		t.Fatal("decoded")
	}
	if msg := decodeProblem(err, &target, "reply"); strings.Contains(msg, "SECRET") {
		t.Errorf("repeats the unmarshaler's quote: %q", msg)
	}
}
