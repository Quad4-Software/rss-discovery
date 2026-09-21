package webhook_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"
	"time"

	"github.com/Quad4-Software/rss-discovery/internal/webhook"
)

func TestVerifySignature(t *testing.T) {
	secret := "whsec"
	body := []byte(`{"event":"feed.updated"}`)
	ts := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	if webhook.Verify(secret, ts, "sha256=abc", body) {
		t.Fatal("bad sig accepted")
	}
	if webhook.Verify("", ts, "sha256=x", body) {
		t.Fatal("empty secret")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(ts + "."))
	_, _ = mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !webhook.Verify(secret, ts, sig, body) {
		t.Fatal("valid sig rejected")
	}
	old := strconv.FormatInt(time.Now().UTC().Add(-10*time.Minute).Unix(), 10)
	mac2 := hmac.New(sha256.New, []byte(secret))
	_, _ = mac2.Write([]byte(old + "."))
	_, _ = mac2.Write(body)
	oldSig := "sha256=" + hex.EncodeToString(mac2.Sum(nil))
	if webhook.Verify(secret, old, oldSig, body) {
		t.Fatal("stale timestamp accepted")
	}
}
