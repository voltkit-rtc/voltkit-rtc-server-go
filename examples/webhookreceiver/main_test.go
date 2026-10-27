package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	voltkit "github.com/voltkit-rtc/voltkit-rtc-server-go"
)

// delivery is a webhook.test event of exactly size bytes, padded in its data.
func delivery(size int) []byte {
	head := `{"id":"evt_` + strconv.Itoa(size) + `","type":"webhook.test","tenantId":"t","createdAt":1787000000000,"data":{"pad":"`
	tail := `"}}`
	return []byte(head + strings.Repeat("x", size-len(head)-len(tail)) + tail)
}

// signed is the X-Stream-Signature VoltKit sends for body, signed now with secret.
func signed(secret string, body []byte) string {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// post delivers body to a receiver holding whsec_test and returns the status and what reached the worker.
func post(body []byte, signature string) (int, int) {
	work := make(chan *voltkit.StreamEvent, 1)
	req := httptest.NewRequest(http.MethodPost, "/hooks/voltkit", bytes.NewReader(body))
	req.Header.Set("X-Stream-Signature", signature)
	rec := httptest.NewRecorder()
	receive([]string{"whsec_test"}, newDedupe(time.Hour), work)(rec, req)
	return rec.Code, len(work)
}

func TestReceiveAnswers413ForABodyOver1MB(t *testing.T) {
	body := delivery(1<<20 + 1)
	if code, queued := post(body, signed("whsec_test", body)); code != http.StatusRequestEntityTooLarge || queued != 0 {
		t.Fatalf("status %d with %d queued, want 413 and nothing queued", code, queued)
	}
}

func TestReceiveTakesASignedDeliveryOfExactly1MB(t *testing.T) {
	body := delivery(1 << 20)
	if code, queued := post(body, signed("whsec_test", body)); code != http.StatusOK || queued != 1 {
		t.Fatalf("status %d with %d queued, want 200 and the event queued", code, queued)
	}
}

func TestReceiveAnswers401ForAnotherSecret(t *testing.T) {
	body := delivery(256)
	if code, queued := post(body, signed("whsec_other", body)); code != http.StatusUnauthorized || queued != 0 {
		t.Fatalf("status %d with %d queued, want 401 and nothing queued", code, queued)
	}
}
