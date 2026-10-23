package voltkit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Spec section 7. The golden vector and the 38 verify cases are asserted from
// the shared vectors too (vectors_test.go).

const (
	goldenBody     = `{"id":"evt_golden","type":"webhook.test","tenantId":"vk_golden","createdAt":1787000000000,"data":{"message":"golden"}}`
	goldenTS       = int64(1787000000)
	goldenCurrent  = "95abd781b3e5d318e78ed06645b74cb19f427e626b33e3c8de4cfa3429541f7a"
	goldenPrevious = "e2b083ef4cb14906ece2bbf046a11c62fbc83696b51f549bcc11735e80c6065c"
)

func at(unix int64) *VerifyOptions {
	return &VerifyOptions{Now: func() time.Time { return time.Unix(unix, 0) }}
}

func lenient(unix int64) *VerifyOptions {
	o := at(unix)
	o.Tolerance = Ptr(100 * 365 * 24 * time.Hour)
	return o
}

func TestWebhooks_GoldenVectorByteForByte(t *testing.T) {
	// The controller's Go tests and the Node tests sign this exact body with
	// these exact secrets; the hex must agree to the byte.
	sign := func(secret string) string {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte("1787000000." + goldenBody))
		return hex.EncodeToString(mac.Sum(nil))
	}
	if sign("whsec_golden_current") != goldenCurrent || sign("whsec_golden_previous") != goldenPrevious {
		t.Fatal("the HMAC does not reproduce the golden signatures")
	}
	header := "t=1787000000,v1=" + goldenCurrent + ",v1=" + goldenPrevious
	if !VerifyWebhook([]byte(goldenBody), header, []string{"whsec_golden_previous"}, at(goldenTS)) {
		t.Error("the SDK does not verify the Go SignAll vector")
	}
	ev, err := ParseWebhook([]byte(goldenBody), header, []string{"whsec_golden_current"}, at(goldenTS))
	if err != nil || ev.ID != "evt_golden" || ev.Type != "webhook.test" || ev.TenantID != "vk_golden" || ev.CreatedAt != 1787000000000 || string(ev.Data) != `{"message":"golden"}` || ev.RoomID != "" {
		t.Errorf("%+v %v", ev, err)
	}
}

func TestWebhooks_HeaderGrammar(t *testing.T) {
	good := "v1=" + goldenCurrent
	body := []byte(goldenBody)
	secret := []string{"whsec_golden_current"}
	if !VerifyWebhook(body, "t=1787000000, "+good, secret, at(goldenTS)) {
		t.Error("spaces after commas are fine")
	}
	if !VerifyWebhook(body, "t=1787000000,v0=deadbeef,"+good, secret, at(goldenTS)) {
		t.Error("an unknown scheme beside v1 is skipped")
	}
	for _, bad := range []string{
		good,                                // no timestamp
		"t=1787000000",                      // no signature
		"t=1787000000,t=1787000000," + good, // two timestamps
		"t=abc," + good,                     // not digits
		"t=1111111111111111," + good,        // 16 digits
		"t=1787000000,v1=XYZ",               // not hex
		"t=1787000000,v1=XYZ," + good,       // one malformed v1 invalidates the header, even beside a good one
		"t=1787000000,v1=" + strings.ToUpper(goldenCurrent) + "," + good, // upper-case hex beside a good one, likewise
		"t=1787000000,v1=" + strings.Repeat("a", 63),                     // wrong length
		"t=1787000000,v1=" + strings.ToUpper(goldenCurrent),              // upper-case hex
		"t=1787000000," + good + ",",                                     // trailing comma
		"t=1787000000,garbage," + good,                                   // a part without =
		"=x,t=1787000000," + good,                                        // an empty key
		"",                                                               // empty
		"garbage",
		"t=1787000000," + good + "," + strings.Repeat("v9=a,", 1000), // over 4096 characters
	} {
		if VerifyWebhook(body, bad, secret, lenient(goldenTS)) {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestWebhooks_FreshnessBothWaysInclusive(t *testing.T) {
	header := "t=1787000000,v1=" + goldenCurrent
	body := []byte(goldenBody)
	secret := []string{"whsec_golden_current"}
	cases := []struct {
		now       int64
		tolerance *time.Duration
		want      bool
	}{
		{goldenTS + 300, nil, true}, {goldenTS + 301, nil, false},
		{goldenTS - 300, nil, true}, {goldenTS - 301, nil, false},
		{goldenTS, Ptr(time.Duration(0)), true}, {goldenTS + 1, Ptr(time.Duration(0)), false},
		{goldenTS + 3600, Ptr(2 * time.Hour), true}, {goldenTS + 3600, Ptr(300 * time.Second), false},
		{2102360000, nil, false}, {2102360000, Ptr(3153600000 * time.Second), true},
	}
	for _, tc := range cases {
		opts := at(tc.now)
		opts.Tolerance = tc.tolerance
		if got := VerifyWebhook(body, header, secret, opts); got != tc.want {
			t.Errorf("now %d tolerance %v: %v", tc.now, tc.tolerance, got)
		}
	}
	// With the real clock, a delivery signed now verifies.
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte("s"))
	mac.Write([]byte(ts + ".x"))
	if !VerifyWebhook([]byte("x"), "t="+ts+",v1="+hex.EncodeToString(mac.Sum(nil)), []string{"s"}, nil) {
		t.Error("a fresh delivery with the default clock and tolerance")
	}
}

func TestWebhooks_RotationEverySecretAgainstEverySignature(t *testing.T) {
	body := []byte(goldenBody)
	both := "t=1787000000,v1=" + goldenCurrent + ",v1=" + goldenPrevious
	one := "t=1787000000,v1=" + goldenCurrent
	for _, tc := range []struct {
		header  string
		secrets []string
		want    bool
	}{
		{both, []string{"whsec_golden_current"}, true},
		{both, []string{"whsec_golden_previous"}, true},
		{both, []string{"whsec_golden_previous", "whsec_golden_current"}, true},
		{both, []string{"whsec_other", "whsec_golden_previous"}, true},
		{both, []string{"whsec_golden_previous", "whsec_other"}, true}, // no early return after a miss or a hit
		{both, []string{"whsec_other", "whsec_nope"}, false},
		{"t=1787000000,v1=" + goldenPrevious + ",v1=" + goldenCurrent, []string{"whsec_golden_current"}, true},
		{one, []string{"whsec_new", "whsec_golden_current"}, true},
		{one, []string{"whsec_golden_previous"}, false},
		{one, []string{"", "whsec_golden_current"}, true}, // empty entries are skipped
	} {
		if got := VerifyWebhook(body, tc.header, tc.secrets, lenient(goldenTS)); got != tc.want {
			t.Errorf("%q with %v: %v", tc.header, tc.secrets, got)
		}
	}
	if VerifyWebhook([]byte(goldenBody+" "), both, []string{"whsec_golden_current"}, lenient(goldenTS)) {
		t.Error("a tampered body (one trailing space) verified")
	}
}

func TestWebhooks_RawBytesOfANonASCIIBody(t *testing.T) {
	body := `{"id":"evt_utf8","type":"chat.message","tenantId":"t","createdAt":1787000001000,"data":{"text":"ü 房間 \"quoted\""}}`
	header := "t=1787000000,v1=0ba687b8f400d063b5f54beaa2fd241c3b75db985e5ab17e27f07f31a43ee6a1"
	if !VerifyWebhook([]byte(body), header, []string{"whsec_test_utf8"}, lenient(goldenTS)) {
		t.Error("the signature covers the UTF-8 bytes")
	}
	if VerifyWebhook([]byte(strings.Replace(body, "房間", "房间", 1)), header, []string{"whsec_test_utf8"}, lenient(goldenTS)) {
		t.Error("one character changed still verified")
	}
}

func TestWebhooks_ProgrammerErrorsAreLoud(t *testing.T) {
	body := []byte(goldenBody)
	header := "t=1787000000,v1=" + goldenCurrent
	for name, tc := range map[string]struct {
		secrets []string
		opts    *VerifyOptions
		want    string
	}{
		"an empty list":        {nil, at(goldenTS), "needs the signing secret"},
		"an empty string":      {[]string{""}, at(goldenTS), "needs the signing secret"},
		"a negative tolerance": {[]string{"whsec_golden_current"}, &VerifyOptions{Tolerance: Ptr(-time.Second)}, "Tolerance must be 0 or more"},
	} {
		func() {
			defer func() {
				r := recover()
				if r == nil {
					t.Errorf("%s: VerifyWebhook did not panic", name)
					return
				}
				if err, ok := r.(error); !ok || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("%s: panicked with %v", name, r)
				}
			}()
			VerifyWebhook(body, header, tc.secrets, tc.opts)
		}()
		_, err := ParseWebhook(body, header, tc.secrets, tc.opts)
		var e *Error
		if err == nil || errors.As(err, &e) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: ParseWebhook must return a plain error naming the mistake, got %v", name, err)
		}
	}
}

func TestWebhooks_ParseFailsWithBadSignature(t *testing.T) {
	body := []byte(goldenBody)
	for _, tc := range []struct {
		header  string
		secrets []string
	}{
		{"t=1787000000,v1=" + goldenCurrent, []string{"whsec_nope"}},
		{"garbage", []string{"whsec_golden_current"}},
	} {
		_, err := ParseWebhook(body, tc.header, tc.secrets, at(goldenTS))
		expectError(t, err, "bad-signature", 401, false, "webhook signature did not verify")
	}
	_, err := ParseWebhook(body, "t=1787000000,v1="+goldenCurrent, []string{"whsec_golden_current"}, at(goldenTS+1000))
	expectError(t, err, "bad-signature", 401, false, "webhook signature did not verify")
	// A verified body that is not JSON is a plain error, not bad-signature.
	mac := hmac.New(sha256.New, []byte("s"))
	mac.Write([]byte("1787000000.not json"))
	_, err = ParseWebhook([]byte("not json"), "t=1787000000,v1="+hex.EncodeToString(mac.Sum(nil)), []string{"s"}, at(goldenTS))
	var e *Error
	if err == nil || errors.As(err, &e) || !strings.Contains(err.Error(), "not JSON") {
		t.Errorf("%v", err)
	}
}
