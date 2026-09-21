package websub_test

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"

	"github.com/Quad4-Software/rss-discovery/internal/websub"
)

func TestDiscoverAndVerify(t *testing.T) {
	h := http.Header{}
	h.Add("Link", `<https://hub.example/hub>; rel="hub", <https://example.com/feed>; rel="self"`)
	d := websub.DiscoverFromHeaders(h)
	if d.Hub != "https://hub.example/hub" || d.Self != "https://example.com/feed" {
		t.Fatalf("%+v", d)
	}
	body := []byte(`<feed><link rel="hub" href="https://hub2.example/"/><link rel="self" href="https://ex.com/atom"/></feed>`)
	b := websub.DiscoverFromBody(body)
	if b.Hub == "" || b.Self == "" {
		t.Fatalf("%+v", b)
	}
	secret := "sekrit"
	payload := []byte("hello")
	if websub.VerifySignature("", payload, "") {
		t.Fatal("empty secret must fail")
	}
	if websub.VerifySignature(secret, payload, "") {
		t.Fatal("empty header must fail")
	}
	mac1 := hmac.New(sha1.New, []byte(secret))
	_, _ = mac1.Write(payload)
	sig1 := "sha1=" + hex.EncodeToString(mac1.Sum(nil))
	if !websub.VerifySignature(secret, payload, sig1) {
		t.Fatal("sha1")
	}
	mac2 := hmac.New(sha256.New, []byte(secret))
	_, _ = mac2.Write(payload)
	sig2 := "sha256=" + hex.EncodeToString(mac2.Sum(nil))
	if !websub.VerifySignature(secret, payload, sig2) {
		t.Fatal("sha256")
	}
}
