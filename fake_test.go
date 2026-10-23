package voltkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fake transport the unit tests drive the client through (test matrix item
// 1): an http.RoundTripper that plays scripted steps, one per attempt (the last
// one repeats), in the step language of the shared vectors, and records every
// request it saw.

const testKey = "sk_test_0123456789abcdef0123456789abcdef"

// step is one scripted reply.
type step struct {
	// network: the request never got a reply (connection refused, DNS, ...).
	network bool
	// errText is the transport's own words for a network step.
	errText string
	// bodyDropped: headers arrived with a 200, the body broke off mid-read.
	bodyDropped bool
	// hangs: no reply until the request's context ends.
	hangs   bool
	status  int
	body    any
	raw     *string
	headers map[string]string
}

func jsonStep(status int, body any) step { return step{status: status, body: body} }

func rawStep(status int, raw string) step { return step{status: status, raw: &raw} }

func errorStep(status int, code, message string) step {
	return jsonStep(status, map[string]any{"error": code, "message": message})
}

// recordedCall is one request as the transport saw it.
type recordedCall struct {
	Method string
	URL    string
	// Path is the path and query as sent, without the origin.
	Path    string
	Headers http.Header
	Body    []byte
	HasBody bool
}

type fakeTransport struct {
	mu    sync.Mutex
	steps []step
	calls []recordedCall
}

// maxFakeRequests bounds a scripted transport. No call makes more than a
// handful of requests (retries plus, for the rates host, a few redirects); a
// client that keeps asking is a defect under test, and this fails it at once
// instead of recording requests until memory runs out, which is what a
// "follow redirects forever" mutant did against a transport whose last step
// (a 302) repeats.
const maxFakeRequests = 64

func scripted(steps ...step) *fakeTransport { return &fakeTransport{steps: steps} }

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	hasBody := req.Body != nil && req.Body != http.NoBody
	if hasBody {
		body, _ = io.ReadAll(req.Body)
		req.Body.Close()
	}
	f.mu.Lock()
	n := len(f.calls)
	if n >= maxFakeRequests {
		f.mu.Unlock()
		panic(fmt.Sprintf("fake transport: runaway: more than %d requests for %s %s", maxFakeRequests, req.Method, req.URL))
	}
	st := f.steps[min(n, len(f.steps)-1)]
	f.calls = append(f.calls, recordedCall{
		Method: req.Method, URL: req.URL.String(), Path: req.URL.RequestURI(),
		Headers: req.Header.Clone(), Body: body, HasBody: hasBody,
	})
	f.mu.Unlock()

	switch {
	case st.hangs:
		<-req.Context().Done()
		return nil, req.Context().Err()
	case st.network:
		text := st.errText
		if text == "" {
			text = "dial tcp 127.0.0.1:1: connect: connection refused"
		}
		return nil, errors.New(text)
	case st.bodyDropped:
		return &http.Response{
			Status: "200 OK", StatusCode: 200, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: http.Header{}, Body: &brokenBody{}, ContentLength: -1, Request: req,
		}, nil
	}
	text := ""
	if st.raw != nil {
		text = *st.raw
	} else if st.body != nil {
		b, err := json.Marshal(st.body)
		if err != nil {
			panic(err)
		}
		text = string(b)
	}
	h := http.Header{}
	for k, v := range st.headers {
		h.Set(k, v)
	}
	return &http.Response{
		Status: fmt.Sprintf("%d %s", st.status, http.StatusText(st.status)), StatusCode: st.status,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: h,
		Body: io.NopCloser(strings.NewReader(text)), ContentLength: int64(len(text)), Request: req,
	}, nil
}

func (f *fakeTransport) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeTransport) call(i int) recordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[i]
}

// brokenBody is a reply body that starts and then fails the way a dropped
// connection does.
type brokenBody struct{ started bool }

func (b *brokenBody) Read(p []byte) (int, error) {
	if !b.started {
		b.started = true
		return copy(p, `{"rooms":`), nil
	}
	return 0, errors.New("unexpected EOF")
}

func (b *brokenBody) Close() error { return nil }

// newClient makes a client on the fake transport with an instant retry clock
// and the jitter pinned to zero, so a test asserts the schedule itself.
func newClient(t testing.TB, ft *fakeTransport, modify ...func(*Config)) *StreamClient {
	t.Helper()
	cfg := Config{BaseURL: "https://x.test/", APIKey: testKey, HTTPClient: &http.Client{Transport: ft}}
	for _, m := range modify {
		m(&cfg)
	}
	c, err := NewStreamClient(cfg)
	if err != nil {
		t.Fatalf("NewStreamClient: %v", err)
	}
	pinClock(c)
	return c
}

// pinClock makes retries instant and the jitter zero; the sleeps asked for
// are recorded and returned.
func pinClock(c *StreamClient) *[]time.Duration {
	var sleeps []time.Duration
	var mu sync.Mutex
	c.sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		sleeps = append(sleeps, d)
		mu.Unlock()
		return ctx.Err()
	}
	c.jitter = func() time.Duration { return 0 }
	return &sleeps
}

func withRetries(n int) func(*Config) { return func(c *Config) { c.Retries = Ptr(n) } }

func withRates(url string) func(*Config) { return func(c *Config) { c.RatesBaseURL = url } }

// asError fails the test unless err is an *Error, which it returns.
func asError(t testing.TB, err error) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("expected a *voltkit.Error, got %T: %v", err, err)
	}
	return e
}

func expectError(t testing.TB, err error, code string, status int, retryable bool, message string) *Error {
	t.Helper()
	e := asError(t, err)
	if e.Code != code || e.Status != status || e.Retryable != retryable {
		t.Errorf("error {code %q status %d retryable %v}, want {%q %d %v}; message %q", e.Code, e.Status, e.Retryable, code, status, retryable, e.Message)
	}
	if message != "" && e.Message != message {
		t.Errorf("message\n got %q\nwant %q", e.Message, message)
	}
	return e
}

var ctxBG = context.Background()
