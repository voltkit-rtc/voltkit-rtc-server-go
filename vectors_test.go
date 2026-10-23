package voltkit

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// The shared vectors (test matrix item 2): one test per section of
// docs/server-sdk-vectors.json, iterating every case. The file is read from the
// repository; a published module does not ship it, so these tests skip when
// it is not there.

type vectorFile struct {
	GeneratedFrom struct {
		Version string `json:"version"`
	} `json:"generatedFrom"`
	Constants struct {
		Defaults struct {
			TimeoutMs        int `json:"timeoutMs"`
			Retries          int `json:"retries"`
			ToleranceSeconds int `json:"toleranceSeconds"`
			UsageSeriesDays  int `json:"usageSeriesDays"`
		} `json:"defaults"`
		Backoff struct {
			BaseMs                  int   `json:"baseMs"`
			Factor                  int   `json:"factor"`
			JitterMaxMs             int   `json:"jitterMaxMs"`
			WaitsWithoutJitterMs    []int `json:"waitsWithoutJitterMs"`
			WorstCaseMsWithDefaults int   `json:"worstCaseMsWithDefaults"`
		} `json:"backoff"`
		MaxTimeoutMs int64 `json:"maxTimeoutMs"`
		Redaction    struct {
			Marker              string `json:"marker"`
			LongestKeyLookedFor int    `json:"longestKeyLookedFor"`
			KeyNotChecked       string `json:"keyNotChecked"`
		} `json:"redaction"`
		MessageCuts struct {
			ErrorReplyWithoutMessage int `json:"errorReplyWithoutMessage"`
			SuccessThatIsNotJSON     int `json:"successThatIsNotJson"`
			RedirectTarget           int `json:"redirectTarget"`
		} `json:"messageCuts"`
		Headers struct {
			APIKey           string `json:"apiKey"`
			ContentType      string `json:"contentType"`
			WebhookSignature string `json:"webhookSignature"`
		} `json:"headers"`
		WebhookHeader struct {
			TimestampKey     string `json:"timestampKey"`
			Scheme           string `json:"scheme"`
			TimestampPattern string `json:"timestampPattern"`
			SignaturePattern string `json:"signaturePattern"`
			MaxHeaderLength  int    `json:"maxHeaderLength"`
		} `json:"webhookHeader"`
		RatesPath string `json:"ratesPath"`
	} `json:"constants"`
	Registry struct {
		ErrorCodes     []string `json:"errorCodes"`
		RESTErrorCodes []string `json:"restErrorCodes"`
		RESTErrors     []struct {
			Code      string `json:"code"`
			Origin    string `json:"origin"`
			HTTP      *int   `json:"http"`
			Retryable bool   `json:"retryable"`
		} `json:"restErrors"`
	} `json:"registry"`
	Webhooks struct {
		Golden struct {
			Body           string `json:"body"`
			Timestamp      int64  `json:"timestamp"`
			CurrentSecret  string `json:"currentSecret"`
			PreviousSecret string `json:"previousSecret"`
			Current        string `json:"current"`
			Previous       string `json:"previous"`
		} `json:"golden"`
		Verify []struct {
			Name             string   `json:"name"`
			Body             string   `json:"body"`
			Header           string   `json:"header"`
			Secrets          []string `json:"secrets"`
			ToleranceSeconds *float64 `json:"toleranceSeconds"`
			NowUnixSeconds   int64    `json:"nowUnixSeconds"`
			Expected         bool     `json:"expected"`
		} `json:"verify"`
		Parse []struct {
			Name     string   `json:"name"`
			Body     string   `json:"body"`
			Header   string   `json:"header"`
			Secrets  []string `json:"secrets"`
			Expected struct {
				Event json.RawMessage `json:"event"`
				Error *vError         `json:"error"`
			} `json:"expected"`
		} `json:"parse"`
		SetupErrors []struct {
			Name             string          `json:"name"`
			Body             json.RawMessage `json:"body"`
			Header           string          `json:"header"`
			Secrets          []string        `json:"secrets"`
			ToleranceSeconds json.RawMessage `json:"toleranceSeconds"`
		} `json:"setupErrors"`
	} `json:"webhooks"`
	APIKeys struct {
		Accepted []struct {
			Name   string `json:"name"`
			Key    string `json:"key"`
			SentAs string `json:"sentAs"`
		} `json:"accepted"`
		Refused []struct {
			Name   string `json:"name"`
			Key    string `json:"key"`
			Reason string `json:"reason"`
		} `json:"refused"`
	} `json:"apiKeys"`
	Options struct {
		TimeoutMs struct {
			Refused []struct {
				TimeoutMs json.RawMessage `json:"timeoutMs"`
			} `json:"refused"`
			Deadlines []struct {
				Name               string          `json:"name"`
				TimeoutMs          json.RawMessage `json:"timeoutMs"`
				EffectiveTimeoutMs int64           `json:"effectiveTimeoutMs"`
			} `json:"deadlines"`
		} `json:"timeoutMs"`
		Retries struct {
			Refused []struct {
				Retries json.RawMessage `json:"retries"`
			} `json:"refused"`
			DefaultAttemptsOnRepeatable503 int `json:"defaultAttemptsOnRepeatable503"`
		} `json:"retries"`
		URLs []struct {
			BaseURL      string  `json:"baseUrl"`
			RatesBaseURL *string `json:"ratesBaseUrl"`
			Expected     struct {
				RoomsURL   string  `json:"roomsUrl"`
				RatesURL   *string `json:"ratesUrl"`
				RatesError *vError `json:"ratesError"`
			} `json:"expected"`
		} `json:"urls"`
	} `json:"options"`
	IDs struct {
		Places []struct {
			Method string `json:"method"`
			Param  string `json:"param"`
		} `json:"places"`
		Refused []struct {
			Method   string `json:"method"`
			Param    string `json:"param"`
			ID       string `json:"id"`
			Expected vError `json:"expected"`
		} `json:"refused"`
		Encoded []struct {
			ID      string `json:"id"`
			Segment string `json:"segment"`
		} `json:"encoded"`
	} `json:"ids"`
	Redaction struct {
		Cases []struct {
			Name           string  `json:"name"`
			Tier           string  `json:"tier"`
			APIKey         string  `json:"apiKey"`
			BaseURL        string  `json:"baseUrl"`
			TransportError string  `json:"transportError"`
			Reply          *vReply `json:"reply"`
			Method         string  `json:"method"`
			Expected       struct {
				Code                  string `json:"code"`
				Status                *int   `json:"status"`
				Retryable             *bool  `json:"retryable"`
				Message               string `json:"message"`
				MessageContains       string `json:"messageContains"`
				MessageMustNotContain string `json:"messageMustNotContain"`
			} `json:"expected"`
		} `json:"cases"`
		ClientPrinting struct {
			APIKey       string `json:"apiKey"`
			BaseURL      string `json:"baseUrl"`
			RatesBaseURL string `json:"ratesBaseUrl"`
			Expected     struct {
				BaseURLShownAs      string `json:"baseUrlShownAs"`
				RatesBaseURLShownAs string `json:"ratesBaseUrlShownAs"`
			} `json:"expected"`
		} `json:"clientPrinting"`
	} `json:"redaction"`
	Retry struct {
		Cases    []vRetryCase `json:"cases"`
		Registry []struct {
			Code      string `json:"code"`
			Status    int    `json:"status"`
			Retryable bool   `json:"retryable"`
		} `json:"registry"`
		BackoffWaitsMs []int `json:"backoffWaitsMs"`
	} `json:"retry"`
	Messages struct {
		Cases []struct {
			Name         string          `json:"name"`
			Method       string          `json:"method"`
			Reply        *vReply         `json:"reply"`
			TimeoutMs    int             `json:"timeoutMs"`
			RatesBaseURL string          `json:"ratesBaseUrl"`
			Body         string          `json:"body"`
			NodeOnly     string          `json:"nodeOnly"`
			Expected     json.RawMessage `json:"expected"`
		} `json:"cases"`
	} `json:"messages"`
	GoneIsDone struct {
		Cases []vRetryCase `json:"cases"`
	} `json:"goneIsDone"`
	Requests struct {
		Cases []struct {
			Method   string         `json:"method"`
			Args     map[string]any `json:"args"`
			Reply    vReply         `json:"reply"`
			Expected struct {
				HTTPMethod string `json:"httpMethod"`
				URL        string `json:"url"`
				Path       string `json:"path"`
				Headers    struct {
					APIKeySent  bool    `json:"apiKeySent"`
					ContentType *string `json:"contentType"`
				} `json:"headers"`
				FollowsRedirects bool            `json:"followsRedirects"`
				Body             json.RawMessage `json:"body"`
				Result           json.RawMessage `json:"result"`
				SafeToRepeat     bool            `json:"safeToRepeat"`
				AttemptsOn503    int             `json:"attemptsOn503"`
			} `json:"expected"`
		} `json:"cases"`
	} `json:"requests"`
	ErrorHelpers struct {
		Cases []struct {
			Code     string          `json:"code"`
			Status   int             `json:"status"`
			Expected map[string]bool `json:"expected"`
		} `json:"cases"`
	} `json:"errorHelpers"`
}

type vError struct {
	Code      string `json:"code"`
	Status    int    `json:"status"`
	Retryable bool   `json:"retryable"`
	Message   string `json:"message"`
}

type vReply struct {
	Network     bool              `json:"network"`
	BodyDropped bool              `json:"bodyDropped"`
	Hangs       bool              `json:"hangs"`
	Status      int               `json:"status"`
	Body        json.RawMessage   `json:"body"`
	Raw         *string           `json:"raw"`
	Headers     map[string]string `json:"headers"`
}

type vRetryCase struct {
	Name     string   `json:"name"`
	Call     string   `json:"call"`
	Retries  int      `json:"retries"`
	Steps    []vReply `json:"steps"`
	Expected struct {
		Attempts int     `json:"attempts"`
		Resolves *bool   `json:"resolves"`
		Error    *vError `json:"error"`
	} `json:"expected"`
}

// step turns a vector reply into a fake-transport step. A JSON body is
// re-serialized compactly, which is what JSON.stringify gave the Node SDK.
func (r vReply) step() step {
	st := step{network: r.Network, bodyDropped: r.BodyDropped, hangs: r.Hangs, status: r.Status, headers: r.Headers}
	if r.Raw != nil {
		st.raw = r.Raw
	} else if len(r.Body) > 0 {
		var compact bytes.Buffer
		if err := json.Compact(&compact, r.Body); err != nil {
			panic(err)
		}
		s := compact.String()
		st.raw = &s
	}
	return st
}

var (
	vectorsOnce sync.Once
	vectorsData *vectorFile
	vectorsErr  error
)

func vectors(t *testing.T) *vectorFile {
	t.Helper()
	vectorsOnce.Do(func() {
		raw, err := os.ReadFile("../docs/server-sdk-vectors.json")
		if err != nil {
			vectorsErr = err
			return
		}
		var v vectorFile
		if err := json.Unmarshal(raw, &v); err != nil {
			vectorsErr = err
			return
		}
		vectorsData = &v
	})
	if vectorsErr != nil {
		if os.IsNotExist(vectorsErr) {
			t.Skip("docs/server-sdk-vectors.json is not beside this module: the shared vectors live in the repository")
		}
		t.Fatal(vectorsErr)
	}
	return vectorsData
}

// invoke calls a method by its vector name with its vector arguments.
func invoke(ctx context.Context, c *StreamClient, method string, a map[string]any) (any, error) {
	str := func(k string) string {
		s, _ := a[k].(string)
		return s
	}
	num := func(k string) int {
		f, _ := a[k].(float64)
		return int(f)
	}
	ret := func(v any, err error) (any, error) {
		if err != nil {
			return nil, err
		}
		return v, nil
	}
	switch strings.Fields(method)[0] {
	case "createRoom":
		return ret(c.CreateRoom(ctx, CreateRoomParams{RoomID: str("roomId"), HostIdentity: str("hostIdentity"), Metadata: a["metadata"]}))
	case "ensureRoom":
		return ret(c.EnsureRoom(ctx, EnsureRoomParams{RoomID: str("roomId"), HostIdentity: str("hostIdentity"), Metadata: a["metadata"]}))
	case "createToken":
		return ret(c.CreateToken(ctx, CreateTokenParams{RoomID: str("roomId"), Identity: str("identity"), Role: Role(str("role"))}))
	case "getRoom":
		return ret(c.GetRoom(ctx, str("roomId")))
	case "listRooms":
		return ret(c.ListRooms(ctx))
	case "listParticipants":
		return ret(c.ListParticipants(ctx, str("roomId")))
	case "deleteRoom":
		return nil, c.DeleteRoom(ctx, str("roomId"))
	case "createChatToken":
		return ret(c.CreateChatToken(ctx, CreateChatTokenParams{Channel: str("channel"), Identity: str("identity"), Role: Role(str("role"))}))
	case "getChatInfo":
		return ret(c.GetChatInfo(ctx, str("channel")))
	case "listMessages":
		var opts *ListMessagesOptions
		if _, ok := a["limit"]; ok {
			opts = &ListMessagesOptions{Limit: num("limit"), Before: str("before")}
		} else if _, ok := a["before"]; ok {
			opts = &ListMessagesOptions{Before: str("before")}
		}
		return ret(c.ListMessages(ctx, str("channel"), opts))
	case "sendMessage":
		eph, _ := a["ephemeral"].(bool)
		return ret(c.SendMessage(ctx, SendMessageParams{Channel: str("channel"), Text: str("text"), Type: str("type"), From: str("from"), To: str("to"), Metadata: a["metadata"], Ephemeral: eph}))
	case "deleteMessage":
		return nil, c.DeleteMessage(ctx, str("channel"), str("messageId"))
	case "listChatMutes":
		return ret(c.ListChatMutes(ctx, str("channel")))
	case "muteChat":
		return nil, c.MuteChat(ctx, str("channel"), str("identity"))
	case "unmuteChat":
		return nil, c.UnmuteChat(ctx, str("channel"), str("identity"))
	case "createStream":
		return ret(c.CreateStream(ctx, CreateStreamParams{StreamID: str("streamId"), Playback: PlaybackPolicy(str("playback")), Metadata: a["metadata"]}))
	case "getStream":
		return ret(c.GetStream(ctx, str("streamId")))
	case "listStreams":
		return ret(c.ListStreams(ctx))
	case "updateStream":
		return ret(c.UpdateStream(ctx, str("streamId"), UpdateStreamParams{Playback: PlaybackPolicy(str("playback")), Metadata: a["metadata"]}))
	case "deleteStream":
		return nil, c.DeleteStream(ctx, str("streamId"))
	case "createStreamToken":
		return ret(c.CreateStreamToken(ctx, CreateStreamTokenParams{StreamID: str("streamId"), Action: StreamAction(str("action")), Identity: str("identity"), TTLSeconds: num("ttlSeconds")}))
	case "getUsage":
		return ret(c.GetUsage(ctx))
	case "getUsageSeries":
		return ret(c.GetUsageSeries(ctx, num("days")))
	case "getBalance":
		return ret(c.GetBalance(ctx))
	case "getRates":
		return ret(c.GetRates(ctx))
	}
	panic("unknown method " + method)
}

// retryArgs are the ids the retry, goneIsDone and redaction sections imply
// through their expected paths (/v1/rooms/r, /v1/chat/c/messages, lobby/1-0,
// lobby/u, /v1/streams/s).
func retryArgs(method string) map[string]any {
	switch method {
	case "createRoom":
		return map[string]any{"hostIdentity": "h"}
	case "ensureRoom", "createToken", "getRoom", "deleteRoom", "listParticipants":
		return map[string]any{"roomId": "r", "hostIdentity": "h", "identity": "u", "role": "viewer"}
	case "sendMessage":
		return map[string]any{"channel": "c", "text": "hi"}
	case "deleteMessage":
		return map[string]any{"channel": "lobby", "messageId": "1-0"}
	case "muteChat", "unmuteChat":
		return map[string]any{"channel": "lobby", "identity": "u"}
	case "createStream":
		return map[string]any{"streamId": "s"}
	case "getStream", "updateStream", "deleteStream", "createStreamToken":
		return map[string]any{"streamId": "s"}
	}
	return map[string]any{}
}

// expectVectorError compares an error with a vector's. The words after
// "METHOD /path: " for a network failure are the transport's own (Node:
// "TypeError: fetch failed", "TypeError: terminated"), so only the prefix is
// shared there (the messages section marks these nodeOnly).
func expectVectorError(t *testing.T, name string, err error, want vError) {
	t.Helper()
	e := asError(t, err)
	if e.Code != want.Code || e.Status != want.Status || e.Retryable != want.Retryable {
		t.Errorf("%s: {%q %d %v}, want {%q %d %v}: %s", name, e.Code, e.Status, e.Retryable, want.Code, want.Status, want.Retryable, e.Message)
	}
	nodeTransport := strings.HasSuffix(want.Message, "TypeError: fetch failed") || strings.HasSuffix(want.Message, "TypeError: terminated")
	if nodeTransport {
		prefix := want.Message[:strings.Index(want.Message, ": ")+2]
		if !strings.HasPrefix(e.Message, prefix) {
			t.Errorf("%s: message %q, want the prefix %q", name, e.Message, prefix)
		}
		return
	}
	// Node names its option baseUrl where this SDK names Config.BaseURL.
	want.Message = strings.ReplaceAll(want.Message, "Point baseUrl at", "Point BaseURL at")
	want.Message = strings.ReplaceAll(want.Message, "getRates needs ratesBaseUrl", "GetRates needs Config.RatesBaseURL")
	want.Message = strings.ReplaceAll(want.Message, "is ratesBaseUrl the", "is Config.RatesBaseURL the")
	if e.Message != want.Message {
		t.Errorf("%s: message\n got %q\nwant %q", name, e.Message, want.Message)
	}
}

func TestVectors_Constants(t *testing.T) {
	v := vectors(t)
	c := v.Constants
	check := func(name string, got, want any) {
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
	check("default timeout", defaultTimeout.Milliseconds(), c.Defaults.TimeoutMs)
	check("default retries", defaultRetries, c.Defaults.Retries)
	check("default tolerance", int(DefaultTolerance.Seconds()), c.Defaults.ToleranceSeconds)
	check("backoff base", backoffBase.Milliseconds(), c.Backoff.BaseMs)
	check("jitter", backoffJitter.Milliseconds(), c.Backoff.JitterMaxMs)
	check("max timeout", maxTimeout.Milliseconds(), c.MaxTimeoutMs)
	check("marker", redactedMarker, c.Redaction.Marker)
	check("longest key", longestKeyLookedFor, c.Redaction.LongestKeyLookedFor)
	check("not checked", keyNotChecked, c.Redaction.KeyNotChecked)
	check("rates path", ratesPath, c.RatesPath)
	check("header", "X-API-Key", c.Headers.APIKey)
	check("webhook header length", maxSignatureHeaderLength, c.WebhookHeader.MaxHeaderLength)
	check("cuts", []int{300, 100, 200}, []int{c.MessageCuts.ErrorReplyWithoutMessage, c.MessageCuts.SuccessThatIsNotJSON, c.MessageCuts.RedirectTarget})
	// The default waits: retries 4 shows all four.
	ft := scripted(errorStep(503, "unavailable", "x"))
	cl := newClient(t, ft, withRetries(4))
	sleeps := pinClock(cl)
	_, _ = cl.GetBalance(ctxBG)
	var ms []int
	for _, d := range *sleeps {
		ms = append(ms, int(d.Milliseconds()))
	}
	check("waits", ms, c.Backoff.WaitsWithoutJitterMs)
	ft = scripted(jsonStep(200, map[string]any{"series": []any{}}))
	_, _ = newClient(t, ft).GetUsageSeries(ctxBG, 0)
	check("usage days", ft.call(0).Path, fmt.Sprintf("/v1/usage/series?days=%d", c.Defaults.UsageSeriesDays))
}

func TestVectors_Registry(t *testing.T) {
	v := vectors(t)
	if fmt.Sprint(ErrorCodes) != fmt.Sprint(v.Registry.ErrorCodes) {
		t.Errorf("ErrorCodes differ from the registry")
	}
	if fmt.Sprint(RESTErrorCodes) != fmt.Sprint(v.Registry.RESTErrorCodes) {
		t.Errorf("RESTErrorCodes differ from the registry")
	}
	for _, r := range v.Registry.RESTErrors {
		info, ok := ErrorInfo(r.Code)
		if !ok || string(info.Origin) != r.Origin || info.Retryable != r.Retryable {
			t.Errorf("%s: %+v, want origin %s retryable %v", r.Code, info, r.Origin, r.Retryable)
		}
	}
	// A reply with each server code is reported with its status and flag.
	for _, r := range v.Retry.Registry {
		ft := scripted(errorStep(r.Status, r.Code, "from the server"))
		_, err := newClient(t, ft, withRetries(0)).GetUsage(ctxBG)
		e := asError(t, err)
		if e.Code != r.Code || e.Status != r.Status || e.Retryable != r.Retryable {
			t.Errorf("%s: %+v", r.Code, e)
		}
	}
}

func TestVectors_Webhooks(t *testing.T) {
	v := vectors(t)
	g := v.Webhooks.Golden
	for _, tc := range []struct{ secret, want string }{{g.CurrentSecret, g.Current}, {g.PreviousSecret, g.Previous}} {
		mac := hmacHex(tc.secret, fmt.Sprintf("%d.%s", g.Timestamp, g.Body))
		if mac != tc.want {
			t.Errorf("golden signature for %s: %s, want %s", tc.secret, mac, tc.want)
		}
	}
	for _, tc := range v.Webhooks.Verify {
		opts := &VerifyOptions{Now: func() time.Time { return time.Unix(tc.NowUnixSeconds, 0) }}
		if tc.ToleranceSeconds != nil {
			opts.Tolerance = Ptr(time.Duration(*tc.ToleranceSeconds * float64(time.Second)))
		}
		if got := VerifyWebhook([]byte(tc.Body), tc.Header, tc.Secrets, opts); got != tc.Expected {
			t.Errorf("verify %q: %v, want %v", tc.Name, got, tc.Expected)
		}
	}
	for _, tc := range v.Webhooks.Parse {
		ev, err := ParseWebhook([]byte(tc.Body), tc.Header, tc.Secrets, &VerifyOptions{Now: func() time.Time { return time.Unix(g.Timestamp, 0) }})
		if tc.Expected.Error != nil {
			expectVectorError(t, tc.Name, err, *tc.Expected.Error)
			continue
		}
		if err != nil {
			t.Errorf("parse %q: %v", tc.Name, err)
			continue
		}
		got, _ := json.Marshal(ev)
		var want StreamEvent
		_ = json.Unmarshal(tc.Expected.Event, &want)
		wantJSON, _ := json.Marshal(want)
		if string(got) != string(wantJSON) {
			t.Errorf("parse %q: %s, want %s", tc.Name, got, wantJSON)
		}
		assertKeysKept(t, tc.Name, tc.Expected.Event, got)
	}
	for _, tc := range v.Webhooks.SetupErrors {
		var body string
		if json.Unmarshal(tc.Body, &body) != nil {
			// "the body is a parsed object, not the raw bytes": a []byte
			// parameter cannot be a parsed object in Go; nothing to test.
			continue
		}
		opts := &VerifyOptions{Now: func() time.Time { return time.Unix(g.Timestamp, 0) }}
		var tol float64
		if json.Unmarshal(tc.ToleranceSeconds, &tol) == nil && len(tc.ToleranceSeconds) > 0 && string(tc.ToleranceSeconds) != "null" {
			opts.Tolerance = Ptr(time.Duration(tol * float64(time.Second)))
		} else if string(tc.ToleranceSeconds) == `"NaN"` {
			continue // a time.Duration cannot be NaN
		}
		_, err := ParseWebhook([]byte(body), tc.Header, tc.Secrets, opts)
		var e *Error
		if err == nil || errors.As(err, &e) {
			t.Errorf("setup error %q: ParseWebhook must fail with a plain error, got %v", tc.Name, err)
		}
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("setup error %q: VerifyWebhook must panic", tc.Name)
				}
			}()
			VerifyWebhook([]byte(body), tc.Header, tc.Secrets, opts)
		}()
	}
}

func hmacHex(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVectors_APIKeys(t *testing.T) {
	v := vectors(t)
	for _, tc := range v.APIKeys.Accepted {
		ft := scripted(jsonStep(200, map[string]any{"rooms": []any{}}))
		c, err := NewStreamClient(Config{BaseURL: "https://x.test", APIKey: tc.Key, HTTPClient: &http.Client{Transport: ft}})
		if err != nil {
			t.Errorf("accepted %q refused: %v", tc.Name, err)
			continue
		}
		pinClock(c)
		if _, err := c.ListRooms(ctxBG); err != nil {
			t.Errorf("%q: %v", tc.Name, err)
			continue
		}
		if got := ft.call(0).Headers.Get("X-API-Key"); got != tc.SentAs {
			t.Errorf("%q: sent %q, want %q", tc.Name, got, tc.SentAs)
		}
	}
	reasons := map[string]string{
		"required": "required", "whitespace-only": "only whitespace",
		"header-unsafe": "no HTTP header can carry", "not-a-voltkit-key": "no VoltKit key has",
	}
	for _, tc := range v.APIKeys.Refused {
		_, err := NewStreamClient(Config{BaseURL: "https://x.test", APIKey: tc.Key})
		if err == nil {
			t.Errorf("refused %q was accepted", tc.Name)
			continue
		}
		if !strings.Contains(err.Error(), reasons[tc.Reason]) {
			t.Errorf("%q: %v, want a message about %s", tc.Name, err, tc.Reason)
		}
		if net := strings.Trim(tc.Key, " \t\r\n"); net != "" && strings.Contains(err.Error(), net) {
			t.Errorf("%q: the message quotes the key", tc.Name)
		}
		if strings.Contains(err.Error(), "8d41f0c2") {
			t.Errorf("%q: the message quotes part of the key", tc.Name)
		}
	}
}

func TestVectors_Options(t *testing.T) {
	v := vectors(t)
	for _, tc := range v.Options.TimeoutMs.Refused {
		var ms float64
		if json.Unmarshal(tc.TimeoutMs, &ms) != nil || ms == 0 {
			// Strings, booleans and NaN cannot be a time.Duration; 0 is the
			// zero value, which means the default in Go.
			continue
		}
		if _, err := NewStreamClient(Config{BaseURL: "https://x.test", APIKey: testKey, Timeout: time.Duration(ms * float64(time.Millisecond))}); err == nil {
			t.Errorf("timeout %v accepted", ms)
		}
	}
	for _, tc := range v.Options.TimeoutMs.Deadlines {
		var timeout time.Duration
		var ms float64
		switch {
		case string(tc.TimeoutMs) == "null":
			timeout = 0
		case string(tc.TimeoutMs) == `"Infinity"`:
			timeout = time.Duration(math.MaxInt64)
		case json.Unmarshal(tc.TimeoutMs, &ms) == nil:
			timeout = time.Duration(ms) * time.Millisecond
		}
		c, err := NewStreamClient(Config{BaseURL: "https://x.test", APIKey: testKey, Timeout: timeout})
		if err != nil || c.timeout.Milliseconds() != tc.EffectiveTimeoutMs {
			t.Errorf("deadline %q: %v (%v), want %d ms", tc.Name, c.timeout, err, tc.EffectiveTimeoutMs)
		}
	}
	for _, tc := range v.Options.Retries.Refused {
		var n float64
		if json.Unmarshal(tc.Retries, &n) != nil || n != math.Trunc(n) {
			continue // not an int: the type refuses it at compile time
		}
		if _, err := NewStreamClient(Config{BaseURL: "https://x.test", APIKey: testKey, Retries: Ptr(int(n))}); err == nil {
			t.Errorf("retries %v accepted", n)
		}
	}
	ft := scripted(errorStep(503, "unavailable", "x"))
	_, _ = newClient(t, ft).GetBalance(ctxBG)
	if ft.count() != v.Options.Retries.DefaultAttemptsOnRepeatable503 {
		t.Errorf("%d attempts by default, want %d", ft.count(), v.Options.Retries.DefaultAttemptsOnRepeatable503)
	}
	for _, tc := range v.Options.URLs {
		rates := ""
		if tc.RatesBaseURL != nil {
			rates = *tc.RatesBaseURL
		}
		ft := scripted(jsonStep(200, map[string]any{"success": true, "data": map[string]any{}}))
		c := newClient(t, ft, func(cfg *Config) { cfg.BaseURL = tc.BaseURL; cfg.RatesBaseURL = rates })
		_, _ = c.ListRooms(ctxBG)
		if got := ft.call(0).URL; got != tc.Expected.RoomsURL {
			t.Errorf("%q: rooms URL %q, want %q", tc.BaseURL, got, tc.Expected.RoomsURL)
		}
		_, err := c.GetRates(ctxBG)
		if tc.Expected.RatesError != nil {
			expectVectorError(t, "rates error for "+rates, err, *tc.Expected.RatesError)
			if ft.count() != 1 {
				t.Errorf("GetRates without RatesBaseURL sent a request")
			}
			continue
		}
		if err != nil || ft.call(1).URL != *tc.Expected.RatesURL {
			t.Errorf("%q: rates URL %q (%v), want %q", rates, ft.call(1).URL, err, *tc.Expected.RatesURL)
		}
	}
}

func TestVectors_IDs(t *testing.T) {
	v := vectors(t)
	if len(v.IDs.Places) != 20 {
		t.Errorf("%d places", len(v.IDs.Places))
	}
	for _, tc := range v.IDs.Refused {
		ft := scripted(jsonStep(200, map[string]any{}))
		args := retryArgs(tc.Method)
		args[tc.Param] = tc.ID
		_, err := invoke(ctxBG, newClient(t, ft), tc.Method, args)
		expectVectorError(t, fmt.Sprintf("%s(%s=%q)", tc.Method, tc.Param, tc.ID), err, tc.Expected)
		if ft.count() != 0 {
			t.Errorf("%s(%s=%q): %d requests were sent", tc.Method, tc.Param, tc.ID, ft.count())
		}
	}
	for _, tc := range v.IDs.Encoded {
		if got := encodeSegment(tc.ID); got != tc.Segment {
			t.Errorf("encodeSegment(%q) = %q, want %q", tc.ID, got, tc.Segment)
		}
		ft := scripted(jsonStep(200, map[string]any{}))
		_, _ = newClient(t, ft).GetRoom(ctxBG, tc.ID)
		if got := ft.call(0).Path; got != "/v1/rooms/"+tc.Segment {
			t.Errorf("GetRoom(%q) sent %q", tc.ID, got)
		}
	}
}

func TestVectors_Redaction(t *testing.T) {
	v := vectors(t)
	for _, tc := range v.Redaction.Cases {
		if tc.Tier != "mandatory" {
			continue
		}
		var ft *fakeTransport
		if tc.Reply != nil {
			ft = scripted(tc.Reply.step())
		} else {
			ft = scripted(step{network: true, errText: tc.TransportError})
		}
		c := newClient(t, ft, withRetries(0), func(cfg *Config) {
			if tc.APIKey != "" {
				cfg.APIKey = tc.APIKey
			}
			if tc.BaseURL != "" {
				cfg.BaseURL = tc.BaseURL
			}
		})
		_, err := invoke(ctxBG, c, tc.Method, retryArgs(tc.Method))
		e := asError(t, err)
		if tc.Expected.Message != "" {
			want := vError{Code: tc.Expected.Code, Status: e.Status, Retryable: e.Retryable, Message: tc.Expected.Message}
			if tc.Expected.Status != nil {
				want.Status = *tc.Expected.Status
			}
			if tc.Expected.Retryable != nil {
				want.Retryable = *tc.Expected.Retryable
			}
			expectVectorError(t, tc.Name, err, want)
		}
		if tc.Expected.MessageContains != "" && !strings.Contains(e.Message, tc.Expected.MessageContains) {
			t.Errorf("%s: %q does not contain %q", tc.Name, e.Message, tc.Expected.MessageContains)
		}
		if tc.Expected.MessageMustNotContain != "" && strings.Contains(e.Message, tc.Expected.MessageMustNotContain) {
			t.Errorf("%s: %q contains %q", tc.Name, e.Message, tc.Expected.MessageMustNotContain)
		}
		if tc.APIKey != "" && len(tc.APIKey) > 8 && strings.Contains(e.Message+e.Code, strings.Trim(tc.APIKey, " \t\r\n")) {
			t.Errorf("%s: the key reached the error", tc.Name)
		}
	}
	p := v.Redaction.ClientPrinting
	ft := scripted(jsonStep(200, map[string]any{}))
	c := newClient(t, ft, func(cfg *Config) { cfg.APIKey = p.APIKey; cfg.BaseURL = p.BaseURL; cfg.RatesBaseURL = p.RatesBaseURL })
	j, _ := json.Marshal(c)
	for _, view := range []string{fmt.Sprintf("%v", c), fmt.Sprintf("%+v", *c), fmt.Sprintf("%#v", c), string(j)} {
		if strings.Contains(view, p.APIKey) || strings.Contains(view, "hunter2") || strings.Contains(view, "s3cret") {
			t.Errorf("printed client shows a secret: %s", view)
		}
		if !strings.Contains(view, p.Expected.BaseURLShownAs) || !strings.Contains(view, p.Expected.RatesBaseURLShownAs) {
			t.Errorf("printed client must show the URLs as %s and %s: %s", p.Expected.BaseURLShownAs, p.Expected.RatesBaseURLShownAs, view)
		}
	}
	_, _ = c.ListRooms(ctxBG)
	if !strings.HasPrefix(ft.call(0).URL, "https://svc:hunter2@x.test/") {
		t.Errorf("requests must still go to the URL as given: %s", ft.call(0).URL)
	}
}

func runRetryCase(t *testing.T, tc vRetryCase) {
	t.Helper()
	steps := make([]step, len(tc.Steps))
	for i, s := range tc.Steps {
		steps[i] = s.step()
	}
	ft := scripted(steps...)
	c := newClient(t, ft, withRetries(tc.Retries))
	_, err := invoke(ctxBG, c, tc.Call, retryArgs(tc.Call))
	if ft.count() != tc.Expected.Attempts {
		t.Errorf("%s: %d attempts, want %d", tc.Name, ft.count(), tc.Expected.Attempts)
	}
	if tc.Expected.Error != nil {
		expectVectorError(t, tc.Name, err, *tc.Expected.Error)
	} else if err != nil {
		t.Errorf("%s: %v, want success", tc.Name, err)
	}
}

func TestVectors_Retry(t *testing.T) {
	v := vectors(t)
	for _, tc := range v.Retry.Cases {
		runRetryCase(t, tc)
	}
	ft := scripted(errorStep(503, "unavailable", "x"))
	c := newClient(t, ft, withRetries(4))
	sleeps := pinClock(c)
	_, _ = c.GetBalance(ctxBG)
	for i, want := range v.Retry.BackoffWaitsMs {
		if int((*sleeps)[i].Milliseconds()) != want {
			t.Errorf("wait %d: %s, want %d ms", i+1, (*sleeps)[i], want)
		}
	}
}

func TestVectors_GoneIsDone(t *testing.T) {
	v := vectors(t)
	for _, tc := range v.GoneIsDone.Cases {
		runRetryCase(t, tc)
	}
}

func TestVectors_Messages(t *testing.T) {
	v := vectors(t)
	for _, tc := range v.Messages.Cases {
		var expected struct {
			vError
			Resolves        json.RawMessage `json:"resolves"`
			ProgrammerError bool            `json:"programmerError"`
			MessagePrefix   string          `json:"messagePrefix"`
			Attempts        *int            `json:"attempts"`
		}
		_ = json.Unmarshal(tc.Expected, &expected)
		args := map[string]any{"roomId": "show-42", "identity": "u", "role": "viewer", "hostIdentity": "h"}
		if strings.Contains(tc.Name, `".."`) {
			args["roomId"] = ".."
		}
		if tc.Body != "" {
			args["metadata"] = make(chan int)
		}
		var ft *fakeTransport
		if tc.Reply != nil {
			ft = scripted(tc.Reply.step())
		} else {
			ft = scripted(jsonStep(200, map[string]any{}))
		}
		c := newClient(t, ft, withRetries(0), func(cfg *Config) {
			cfg.RatesBaseURL = tc.RatesBaseURL
			if tc.TimeoutMs > 0 {
				cfg.Timeout = time.Duration(tc.TimeoutMs) * time.Millisecond
			}
		})
		_, err := invoke(ctxBG, c, tc.Method, args)
		switch {
		case expected.ProgrammerError:
			var e *Error
			if err == nil || errors.As(err, &e) || !strings.HasPrefix(err.Error(), expected.MessagePrefix) {
				t.Errorf("%s: want a plain error starting %q, got %v", tc.Name, expected.MessagePrefix, err)
			}
			if expected.Attempts != nil && ft.count() != *expected.Attempts {
				t.Errorf("%s: %d attempts, want %d", tc.Name, ft.count(), *expected.Attempts)
			}
		case len(expected.Resolves) > 0:
			if err != nil {
				t.Errorf("%s: %v", tc.Name, err)
			}
		default:
			expectVectorError(t, tc.Name, err, expected.vError)
		}
	}
}

func TestVectors_Requests(t *testing.T) {
	v := vectors(t)
	for _, tc := range v.Requests.Cases {
		ft := scripted(tc.Reply.step())
		c := newClient(t, ft, withRates("https://api.example.com"))
		got, err := invoke(ctxBG, c, tc.Method, tc.Args)
		if err != nil {
			t.Errorf("%s: %v", tc.Method, err)
			continue
		}
		r := ft.call(0)
		if r.Method != tc.Expected.HTTPMethod || r.URL != tc.Expected.URL || r.Path != tc.Expected.Path {
			t.Errorf("%s: sent %s %s (%s), want %s %s (%s)", tc.Method, r.Method, r.URL, r.Path, tc.Expected.HTTPMethod, tc.Expected.URL, tc.Expected.Path)
		}
		if (r.Headers.Get("X-API-Key") != "") != tc.Expected.Headers.APIKeySent {
			t.Errorf("%s: X-API-Key sent %v", tc.Method, !tc.Expected.Headers.APIKeySent)
		}
		wantCT := ""
		if tc.Expected.Headers.ContentType != nil {
			wantCT = *tc.Expected.Headers.ContentType
		}
		if r.Headers.Get("Content-Type") != wantCT {
			t.Errorf("%s: Content-Type %q, want %q", tc.Method, r.Headers.Get("Content-Type"), wantCT)
		}
		if string(tc.Expected.Body) == "null" {
			if r.HasBody {
				t.Errorf("%s: sent a body %s", tc.Method, r.Body)
			}
		} else {
			var want, sent any
			_ = json.Unmarshal(tc.Expected.Body, &want)
			if err := json.Unmarshal(r.Body, &sent); err != nil || !reflect.DeepEqual(want, sent) {
				t.Errorf("%s: body %s, want %s", tc.Method, r.Body, tc.Expected.Body)
			}
		}
		assertResult(t, tc.Method, got, tc.Expected.Result)
		// Retried or not: a 503 on the same call.
		ft2 := scripted(errorStep(503, "unavailable", "x"))
		_, _ = invoke(ctxBG, newClient(t, ft2, withRates("https://api.example.com")), tc.Method, tc.Args)
		if ft2.count() != tc.Expected.AttemptsOn503 {
			t.Errorf("%s: %d attempts on 503, want %d", tc.Method, ft2.count(), tc.Expected.AttemptsOn503)
		}
	}
}

// assertResult compares what a method returned with the vector's result: both
// are viewed through the method's Go type (so the comparison is exact for
// every field the type carries), and every key of the vector's JSON must be
// present in the SDK's, nested, so a documented field cannot be dropped by a
// missing struct field.
func assertResult(t *testing.T, name string, got any, want json.RawMessage) {
	t.Helper()
	if string(want) == "null" {
		if got != nil {
			t.Errorf("%s: resolved to %v, want nothing", name, got)
		}
		return
	}
	if got == nil {
		t.Errorf("%s: resolved to nothing, want %s", name, want)
		return
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	fresh := reflect.New(reflect.TypeOf(got))
	if err := json.Unmarshal(want, fresh.Interface()); err != nil {
		t.Errorf("%s: the expected result does not fit %T: %v", name, got, err)
		return
	}
	wantJSON, _ := json.Marshal(fresh.Elem().Interface())
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("%s:\n got %s\nwant %s", name, gotJSON, wantJSON)
	}
	assertKeysKept(t, name, want, gotJSON)
}

func assertKeysKept(t *testing.T, name string, want, got json.RawMessage) {
	t.Helper()
	var w, g any
	_ = json.Unmarshal(want, &w)
	_ = json.Unmarshal(got, &g)
	var walk func(path string, w, g any)
	walk = func(path string, w, g any) {
		switch wv := w.(type) {
		case map[string]any:
			gv, ok := g.(map[string]any)
			if !ok {
				t.Errorf("%s: %s is not an object in the SDK's result", name, path)
				return
			}
			for k, v := range wv {
				child, present := gv[k]
				if !present {
					t.Errorf("%s: the field %s.%s is dropped by the typed model", name, path, k)
					continue
				}
				walk(path+"."+k, v, child)
			}
		case []any:
			gv, ok := g.([]any)
			if !ok || len(gv) != len(wv) {
				t.Errorf("%s: %s differs in length", name, path)
				return
			}
			for i := range wv {
				walk(fmt.Sprintf("%s[%d]", path, i), wv[i], gv[i])
			}
		}
	}
	walk("result", w, g)
}

func TestVectors_ErrorHelpers(t *testing.T) {
	v := vectors(t)
	for _, tc := range v.ErrorHelpers.Cases {
		e := &Error{Code: tc.Code, Status: tc.Status}
		got := map[string]bool{
			"isNotFound": e.IsNotFound(), "isOutOfMinutes": e.IsOutOfMinutes(), "isQuota": e.IsQuota(),
			"isCapacityExhausted": e.IsCapacityExhausted(), "isBroadcastDisabled": e.IsBroadcastDisabled(), "isBillingUnavailable": e.IsBillingUnavailable(),
		}
		for k, want := range tc.Expected {
			if got[k] != want {
				t.Errorf("%s %d: %s = %v, want %v", tc.Code, tc.Status, k, got[k], want)
			}
		}
	}
}
