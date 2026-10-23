package voltkit

import (
	"strings"
	"testing"
)

// Spec section 5.5. The mandatory forms are asserted case by case from the
// shared vectors (vectors_test.go); these name the rules.

func TestRedact_TheKeyInItsEncodings(t *testing.T) {
	key := "sk_live_Zm9v+YmFyLzk/QmF6+cXV4/dGVzdF9rZXk+MTIz/NDU2Nzg=="
	r := newKeyRedactor(key)
	for _, form := range []string{
		key,
		"sk_live_Zm9v%2BYmFyLzk%2FQmF6%2BcXV4%2FdGVzdF9rZXk%2BMTIz%2FNDU2Nzg%3D%3D",
		"sk_live_Zm9v%2bYmFyLzk%2fQmF6%2bcXV4%2fdGVzdF9rZXk%2bMTIz%2fNDU2Nzg%3d%3d",
		"sk_live_Zm9v+YmFyLzk\\/QmF6+cXV4\\/dGVzdF9rZXk+MTIz\\/NDU2Nzg==",
		"sk_live_Zm9v+YmFyLzk&#x2F;QmF6+cXV4&#x2F;dGVzdF9rZXk+MTIz&#x2F;NDU2Nzg==",
		"sk_live_Zm9v&plus;YmFyLzk&sol;QmF6&plus;cXV4&sol;dGVzdF9rZXk&plus;MTIz&sol;NDU2Nzg==",
		"sk_live_Zm9v%252BYmFyLzk%252FQmF6%252BcXV4%252FdGVzdF9rZXk%252BMTIz%252FNDU2Nzg%253D%253D",
	} {
		if got := r.redact("bad key " + form + " given"); got != "bad key [redacted] given" {
			t.Errorf("%q -> %q", form, got)
		}
	}
	// Near misses and unrelated escapes are kept exactly.
	for _, kept := range []string{
		"k_live_Zm9v%2BYmFyLzk%2FQmF6%2BcXV4%2FdGVzdF9rZXk%2BMTIz%2FNDU2Nzg%3D%3D",
		"sk_live_Zm9v+YmFyLzk/QmF6+cXV4/dGVzdF9rZXk+MTIz/NDU2Nzg / Zm9v",
		"GET https://voltkit.in/docs?a=1&b=%2F%2b%252F#top failed",
		`a \" b \/ c \u0026 d \\ e \\\/ f \&quot; g`,
		"<p>Tom &amp; Jerry &#x2F; &#47; &#X5F; &quot;hi&quot;</p>",
	} {
		if got := r.redact(kept); got != kept {
			t.Errorf("changed %q to %q", kept, got)
		}
	}
}

func TestRedact_EveryPrintableCharacterAsHTMLNames(t *testing.T) {
	key := "sk_ !\"#$%&'()*+,-./0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[]^_`abcdefghijklmnopqrstuvwxyz{|}~"
	r := newKeyRedactor(key)
	named := "sk&lowbar; &excl;&quot;&num;&dollar;&percnt;&amp;&apos;&lpar;&rpar;&ast;&plus;&comma;-&period;&sol;0123456789&colon;&semi;&lt;&equals;&gt;&quest;&commat;ABCDEFGHIJKLMNOPQRSTUVWXYZ&lsqb;&rsqb;&Hat;&lowbar;&grave;abcdefghijklmnopqrstuvwxyz&lcub;&verbar;&rcub;~"
	if got := r.redact("bad key " + named); got != "bad key [redacted]" {
		t.Errorf("%q", got)
	}
	if got := r.redact("bad key " + key); got != "bad key [redacted]" {
		t.Errorf("%q", got)
	}
}

func TestRedact_ATabInsideTheKey(t *testing.T) {
	r := newKeyRedactor("sk_a\tb_0123456789abcdef")
	for _, form := range []string{"sk_a\tb_0123456789abcdef", `sk_a\tb_0123456789abcdef`, "sk_a%09b_0123456789abcdef", "sk_a&Tab;b_0123456789abcdef"} {
		if got := r.redact("bad key " + form + " given"); got != "bad key [redacted] given" {
			t.Errorf("%q -> %q", form, got)
		}
	}
}

func TestRedact_AKeyOver256CharactersWithholdsWordsAsLongAsIt(t *testing.T) {
	exactly := strings.Repeat("sk_live_Ab+/=", 19) + "xyz" // 19*13 + 3 = 250... padded below
	exactly += strings.Repeat("x", 256-len(exactly))
	if len(exactly) != 256 {
		t.Fatal("test key length")
	}
	if got := newKeyRedactor(exactly).redact("bad key " + exactly); got != "bad key [redacted]" {
		t.Errorf("a key of exactly 256 is looked for: %q", got)
	}
	long := exactly + "!"
	r := newKeyRedactor(long)
	if got := r.redact("bad key " + long + " rejected"); got != "bad key "+keyNotChecked+" rejected" {
		t.Errorf("the word holding it must be withheld: %q", got)
	}
	if got := r.redact("room not found"); got != "room not found" {
		t.Errorf("short words are kept: %q", got)
	}
	encoded := strings.ReplaceAll(strings.ReplaceAll(long, "+", "%2B"), "/", "%2F")
	if got := r.redact(`bad key "k=` + encoded + `"`); got != "bad key "+keyNotChecked {
		t.Errorf("a word holding the key encoded is withheld: %q", got)
	}
	// With a tab inside, the whole message goes, unless it is shorter than the key.
	tabbed := long[:100] + "\t" + long[101:]
	r = newKeyRedactor(tabbed)
	if got := r.redact("bad key " + strings.ReplaceAll(tabbed, "\t", " ") + " given"); got != keyNotChecked {
		t.Errorf("%q", got)
	}
	if got := r.redact("room not found"); got != "room not found" {
		t.Errorf("a message shorter than the key is kept: %q", got)
	}
}

func TestRedact_URLCredentials(t *testing.T) {
	for in, want := range map[string]string{
		"see https://user:pass@host.test/path":              "see https://[redacted]@host.test/path",
		"HTTP://U:P@H and ws://a@b and wss://x:y@z/q?k=1":   "HTTP://[redacted]@H and ws://[redacted]@b and wss://[redacted]@z/q?k=1",
		"git+ssh://me@host and custom.v1-x://u@h":           "git+ssh://[redacted]@host and custom.v1-x://[redacted]@h",
		"mailto:me@host.test is not a URL with credentials": "mailto:me@host.test is not a URL with credentials",
		"https://host.test/a@b in the path":                 "https://host.test/a@b in the path",
		"no scheme user@host":                               "no scheme user@host",
		"http://@empty and https://u@h@i/x":                 "http://[redacted]@empty and https://[redacted]@i/x",
		"https://admin:hunter2@pw@127.0.0.1:9/":             "https://[redacted]@127.0.0.1:9/",
		"1abc://u@h":                                        "1abc://[redacted]@h",
		"123://u@h":                                         "123://u@h",
	} {
		if got := redactURLCredentials(in); got != want {
			t.Errorf("%q\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestRedact_HappensBeforeTheCut(t *testing.T) {
	// A key at the end of a 400-character page: redacted first, then cut at
	// 300, so the cut cannot leave the key's tail behind.
	ft := scripted(rawStep(502, strings.Repeat(".", 290)+testKey))
	_, err := newClient(t, ft, withRetries(0)).ListStreams(ctxBG)
	e := asError(t, err)
	if strings.Contains(e.Message, testKey[len(testKey)-8:]) || !strings.HasSuffix(e.Message, "[redacted]") {
		t.Errorf("%q", e.Message)
	}
	if got := cutRunes("héllo", 2); got != "hé" {
		t.Errorf("cutRunes counts characters, not bytes: %q", got)
	}
}

func TestRedact_TheTransportsOwnWordsAndTheCode(t *testing.T) {
	ft := scripted(step{network: true, errText: "request to https://deploy:hunter2@x.test/v1/billing/balance failed with key " + testKey})
	c := newClient(t, ft, func(cfg *Config) { cfg.BaseURL = "https://deploy:hunter2@x.test" })
	_, err := c.GetBalance(ctxBG)
	e := expectError(t, err, "network", 0, true, "")
	if strings.Contains(e.Message, "hunter2") || strings.Contains(e.Message, testKey) || !strings.Contains(e.Message, "https://[redacted]@x.test/v1/billing/balance failed with key [redacted]") {
		t.Errorf("%q", e.Message)
	}
	ft = scripted(jsonStep(401, map[string]any{"error": "Invalid API key: " + testKey}))
	_, err = newClient(t, ft).ListRooms(ctxBG)
	expectError(t, err, "Invalid API key: [redacted]", 401, false, `GET /v1/rooms: {"error":"Invalid API key: [redacted]"}`)
	ft = scripted(step{status: 302, headers: map[string]string{"Location": "https://elsewhere.test/?key=" + testKey}})
	_, err = newClient(t, ft).GetBalance(ctxBG)
	e = expectError(t, err, "redirected", 302, false, "")
	if !strings.Contains(e.Message, "to https://elsewhere.test/?key=[redacted];") {
		t.Errorf("%q", e.Message)
	}
}
