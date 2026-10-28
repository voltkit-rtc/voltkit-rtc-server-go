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

// SDK red team, 2026-10-28 (the Music add-on round, 0.1.2): GetRates against a
// hostile rates host -- the addons object in every wrong shape, numbers JSON
// cannot keep, deep nesting, a 10 MB reply -- and the secrets (the API key, URL
// credentials) after those paths.

const rtMusic = `{"id":"music","label":"Music add-on","price":"10.00","cuPerParticipantSecond":"1000",` +
	`"unit":"credits per 1,000 participant-minutes while music plays","appliesTo":["audio_std","audio_hd"],` +
	`"includedIn":["video_30","video_480_60","video_720_60","video_1080_60"]}`

func rtRates(addons string) string {
	return `{"success":true,"data":{"version":4,"kind":"standard","effectiveAt":null,"tiers":[],"surcharges":[],` +
		`"addons":` + addons + `,"idleTier":"audio_std","modes":[],"notes":[]}}`
}

func rtMusicWith(old, replacement string) string {
	if !strings.Contains(rtMusic, old) {
		panic(old)
	}
	return `{"music":` + strings.Replace(rtMusic, old, replacement, 1) + `}`
}

func rtGetRates(t *testing.T, raw string) (*RtcRates, error, *fakeTransport) {
	t.Helper()
	ft := scripted(rawStep(200, raw))
	rates, err := newClient(t, ft, withRates("https://api.example.com"), withRetries(0)).GetRates(ctxBG)
	return rates, err, ft
}

// Every wrong type is refused with the SDK's own error (http-200, not
// retried), never decoded half-way, coerced or a panic.
func TestRedteam_WrongTypesInAddonsAreRefusedTyped(t *testing.T) {
	for name, addons := range map[string]string{
		"addons a string":                         `"music"`,
		"addons a number":                         `10`,
		"addons true":                             `true`,
		"addons an empty list":                    `[]`,
		"addons a list of the add-on":             `[` + rtMusic + `]`,
		"music a string":                          `{"music":"10.00"}`,
		"music a number":                          `{"music":1000}`,
		"music a list":                            `{"music":[` + rtMusic + `]}`,
		"price a number":                          rtMusicWith(`"price":"10.00"`, `"price":10.1`),
		"price 1e309":                             rtMusicWith(`"price":"10.00"`, `"price":1e309`),
		"price -0":                                rtMusicWith(`"price":"10.00"`, `"price":-0`),
		"price an object":                         rtMusicWith(`"price":"10.00"`, `"price":{"amount":"10.00"}`),
		"price a list":                            rtMusicWith(`"price":"10.00"`, `"price":["10.00"]`),
		"price true":                              rtMusicWith(`"price":"10.00"`, `"price":true`),
		"cuPerParticipantSecond a huge integer":   rtMusicWith(`"cuPerParticipantSecond":"1000"`, `"cuPerParticipantSecond":123456789012345678901234567890`),
		"cuPerParticipantSecond a float":          rtMusicWith(`"cuPerParticipantSecond":"1000"`, `"cuPerParticipantSecond":1000.5`),
		"unit a number":                           rtMusicWith(`"unit":"credits per 1,000 participant-minutes while music plays"`, `"unit":5`),
		"appliesTo a string":                      rtMusicWith(`"appliesTo":["audio_std","audio_hd"]`, `"appliesTo":"audio_std,audio_hd"`),
		"appliesTo with a number":                 rtMusicWith(`"appliesTo":["audio_std","audio_hd"]`, `"appliesTo":["audio_std",1]`),
		"appliesTo with a bool":                   rtMusicWith(`"appliesTo":["audio_std","audio_hd"]`, `"appliesTo":["audio_std",true]`),
		"appliesTo with an object":                rtMusicWith(`"appliesTo":["audio_std","audio_hd"]`, `"appliesTo":["audio_std",{}]`),
		"includedIn an object":                    rtMusicWith(`"includedIn":["video_30","video_480_60","video_720_60","video_1080_60"]`, `"includedIn":{"video_30":true}`),
		"id a number":                             rtMusicWith(`"id":"music"`, `"id":7`),
		"label a list":                            rtMusicWith(`"label":"Music add-on"`, `"label":["Music"]`),
		"NaN literal":                             rtMusicWith(`"price":"10.00"`, `"price":NaN`),
		"Infinity literal":                        rtMusicWith(`"price":"10.00"`, `"price":Infinity`),
		"nested 20,000 deep":                      `{"music":` + strings.Repeat("[", 20000) + strings.Repeat("]", 20000) + `}`,
		"truncated":                               `{"music":{"price":"10.00"`,
		"trailing garbage after the rates object": rtMusic + ` xx`,
	} {
		rates, err, ft := rtGetRates(t, rtRates(addons))
		if err == nil {
			t.Errorf("%s: decoded as %+v; want the SDK's error", name, rates)
			continue
		}
		var e *Error
		if !errors.As(err, &e) {
			t.Errorf("%s: %T %v, not a *voltkit.Error", name, err, err)
			continue
		}
		if e.Code != "http-200" || e.Status != 200 || e.Retryable || rates != nil || ft.count() != 1 {
			t.Errorf("%s: %+v rates=%v after %d attempts", name, e, rates, ft.count())
		}
	}
}

// What decodes keeps the API's exact words: a price that is a string stays
// that string, nothing is rounded, split or filled in.
func TestRedteam_OddButWellTypedAddonsDecodeExactly(t *testing.T) {
	cases := []struct {
		name   string
		addons string
		check  func(*RtcRates) error
	}{
		{"addons null", `null`, func(r *RtcRates) error {
			if r.Addons != nil {
				return fmt.Errorf("addons %+v", r.Addons)
			}
			return nil
		}},
		{"addons empty", `{}`, func(r *RtcRates) error {
			if r.Addons == nil || r.Addons.Music != nil {
				return fmt.Errorf("addons %+v", r.Addons)
			}
			return nil
		}},
		{"music null", `{"music":null}`, func(r *RtcRates) error {
			if r.Addons == nil || r.Addons.Music != nil {
				return fmt.Errorf("addons %+v", r.Addons)
			}
			return nil
		}},
		{"music with every field missing", `{"music":{}}`, func(r *RtcRates) error {
			m := r.Addons.Music
			if m == nil || m.Price != "" || m.CuPerParticipantSecond != "" || m.AppliesTo != nil || m.IncludedIn != nil {
				return fmt.Errorf("music %+v", m)
			}
			return nil
		}},
		{"price a string past a double", rtMusicWith(`"price":"10.00"`, `"price":"1e309"`), func(r *RtcRates) error {
			if r.Addons.Music.Price != "1e309" {
				return fmt.Errorf("price %q", r.Addons.Music.Price)
			}
			return nil
		}},
		{"cuPerParticipantSecond a 5,000-digit string", rtMusicWith(`"cuPerParticipantSecond":"1000"`, `"cuPerParticipantSecond":"`+strings.Repeat("9", 5000)+`"`), func(r *RtcRates) error {
			cu, err := ParseCu(r.Addons.Music.CuPerParticipantSecond)
			if err != nil || len(cu.String()) != 5000 {
				return fmt.Errorf("cu %v %v", cu, err)
			}
			return nil
		}},
		{"unicode escapes in the id", rtMusicWith(`"id":"music"`, `"id":"music\u0000"`), func(r *RtcRates) error {
			if r.Addons.Music.ID != "music\x00" {
				return fmt.Errorf("id %q", r.Addons.Music.ID)
			}
			return nil
		}},
		{"an unknown add-on beside music (not modelled: dropped)", `{"music":` + rtMusic + `,"karaoke":{"price":"99.00"}}`, func(r *RtcRates) error {
			if r.Addons.Music.Price != "10.00" {
				return fmt.Errorf("music %+v", r.Addons.Music)
			}
			return nil
		}},
		{"__proto__ and constructor keys are plain unknown keys", `{"__proto__":{"price":"0.00"},"constructor":{"prototype":{"price":"0.00"}},"music":` + rtMusic + `}`, func(r *RtcRates) error {
			if r.Addons.Music.Price != "10.00" {
				return fmt.Errorf("music %+v", r.Addons.Music)
			}
			return nil
		}},
	}
	for _, tc := range cases {
		rates, err, _ := rtGetRates(t, rtRates(tc.addons))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if err := tc.check(rates); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// ParseCu, which the README tells callers to use on the add-on's
// cuPerParticipantSecond, refuses every form that is not a decimal integer.
func TestRedteam_ParseCuOnHostileAddonPrices(t *testing.T) {
	for _, bad := range []string{"", " ", "1e3", "1000.0", "1,000", "1_000", "0x3e8", "-+5", "+-5", "--5", "١٠٠٠", "１０００", "NaN", "Infinity", "10.00", "1000\x00"} {
		if n, err := ParseCu(bad); err == nil {
			t.Errorf("ParseCu(%q) = %v; want an error", bad, n)
		}
	}
	for in, want := range map[string]string{"1000": "1000", " 1000\n": "1000", "-0": "0", "+7": "7", "007": "7"} {
		n, err := ParseCu(in)
		if err != nil || n.String() != want {
			t.Errorf("ParseCu(%q) = %v, %v; want %s", in, n, err, want)
		}
	}
}

// A 10 MB rates reply is over the 8 MiB cap: refused with the SDK's error, not
// read to the end, never a crash.
func TestRedteam_A10MBRatesReplyIsRefusedByTheCap(t *testing.T) {
	big := rtRates(`{"music":` + strings.Replace(rtMusic, `"label":"Music add-on"`, `"label":"`+strings.Repeat("m", 10<<20)+`"`, 1) + `}`)
	started := time.Now()
	_, err, ft := rtGetRates(t, big)
	e := asError(t, err)
	if e.Code != "http-200" || !strings.Contains(e.Message, "too large") || ft.count() != 1 {
		t.Errorf("%+v after %d attempts", e, ft.count())
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Errorf("took %v", took)
	}
}

// After the hostile paths, nothing shows the key or a URL password: the
// message, every fmt verb on the error and the client, errors.Join, JSON.
func TestRedteam_SecretsAfterHostileRatesReplies(t *testing.T) {
	const key = "sk_8d41f0c2b97e3a65d0f1c4e7a92b5d38c6e1f04a7b29d3c58e60f1a2b4c7d9e0"
	quoted := key + " https://web:s3cret@api.example.com/x https://svc:hunter2@x.test/v1"
	steps := map[string]step{
		"200 not JSON quoting both":           rawStep(200, "<pre>"+quoted+"</pre>"),
		"200 truncated JSON with the key":     rawStep(200, rtRates(`{"music":{"price":"`+key)),
		"200 a wrong type beside the key":     rawStep(200, rtRates(`{"music":{"unit":"`+key+`","price":12345678901234567890}}`)),
		"200 a key-named field, wrong type":   rawStep(200, rtRates(`{"music":{"`+key+`":1,"price":[1]}}`)),
		"500 code and message quoting both":   jsonStep(500, map[string]any{"success": false, "error": map[string]any{"code": key, "message": quoted}}),
		"503 page quoting both":               rawStep(503, "<h1>down</h1>"+quoted),
		"network error quoting the url+key":   {network: true, errText: "Get \"https://web:s3cret@api.example.com/api/v1/billing/rtc/rates/current\": dial: refused " + key},
		"10 MB 502 page with both at the end": rawStep(502, strings.Repeat("x", 10<<20)+" "+quoted),
	}
	for name, st := range steps {
		ft := scripted(st)
		c := newClient(t, ft, withRetries(0), func(cfg *Config) {
			cfg.APIKey = key
			cfg.BaseURL = "https://svc:hunter2@x.test"
			cfg.RatesBaseURL = "https://web:s3cret@api.example.com"
		})
		rates, err := c.GetRates(ctxBG)
		if err == nil {
			t.Errorf("%s: decoded %+v", name, rates)
			continue
		}
		asError(t, err)
		var views []string
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			views = append(views, fmt.Sprintf(verb, err), fmt.Sprintf(verb, c))
		}
		j, _ := json.Marshal(err)
		views = append(views, string(j), errors.Join(err, fmt.Errorf("wrapped: %w", err)).Error())
		for _, v := range views {
			for _, secret := range []string{key, key[10:30], "hunter2", "s3cret"} {
				if strings.Contains(v, secret) {
					t.Errorf("%s: shows %q: %.300s", name, secret, v)
				}
			}
		}
		for i := 0; i < ft.count(); i++ {
			if got := ft.call(i).Headers.Get("X-API-Key"); got != "" {
				t.Errorf("%s: the rates host was sent the key", name)
			}
		}
	}
}

// Duplicate keys: the last value wins for a plain field, as in the other SDKs.
// (For a duplicated object Go merges the two; Node, Python and PHP keep the
// last object whole. Pinned so a change is seen.)
func TestRedteam_DuplicateKeysInTheAddon(t *testing.T) {
	rates, err, _ := rtGetRates(t, rtRates(rtMusicWith(`"price":"10.00"`, `"price":"10.00","price":"0.00"`)))
	if err != nil || rates.Addons.Music.Price != "0.00" {
		t.Fatalf("%v %v", rates, err)
	}
	rates, err, _ = rtGetRates(t, rtRates(`{"music":`+rtMusic+`,"music":{"price":"99.00"}}`))
	if err != nil {
		t.Fatal(err)
	}
	m := rates.Addons.Music
	if m.Price != "99.00" || !reflect.DeepEqual(m.AppliesTo, []string{"audio_std", "audio_hd"}) {
		t.Errorf("merged: %+v", m)
	}
}
