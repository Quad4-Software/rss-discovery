package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Quad4-Software/rss-discovery/internal/security"
	"github.com/Quad4-Software/rss-discovery/internal/store"
)

// Dispatcher sends signed webhook callbacks.
type Dispatcher struct {
	HTTP    *http.Client
	Store   *store.Store
	Timeout time.Duration
}

// Event payload for feed.updated.
type Event struct {
	Event     string    `json:"event"`
	FeedID    string    `json:"feed_id"`
	URL       string    `json:"url"`
	Title     string    `json:"title,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// NotifyFeedUpdated fans out to matching webhooks.
func (d *Dispatcher) NotifyFeedUpdated(ctx context.Context, feed store.Feed) {
	if d == nil || d.Store == nil {
		return
	}
	hooks, err := d.Store.MatchingWebhooks(ctx, "feed.updated", feed.ID)
	if err != nil || len(hooks) == 0 {
		return
	}
	ev := Event{
		Event:     "feed.updated",
		FeedID:    feed.ID,
		URL:       feed.URL,
		Title:     feed.Title,
		Timestamp: time.Now().UTC(),
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return
	}
	client := d.HTTP
	if client == nil {
		to := d.Timeout
		if to <= 0 {
			to = 10 * time.Second
		}
		client = &http.Client{Timeout: to}
	}
	for _, wh := range hooks {
		go d.deliver(context.Background(), client, wh, "feed.updated", payload)
	}
}

func (d *Dispatcher) deliver(ctx context.Context, _ *http.Client, wh store.Webhook, event string, payload []byte) {
	if err := security.ValidatePublicHTTPSURL(wh.URL); err != nil {
		_ = d.Store.RecordWebhookDelivery(ctx, wh.ID, event, string(payload), 0, err.Error())
		return
	}
	ts := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(wh.Secret))
	_, _ = mac.Write([]byte(ts + "."))
	_, _ = mac.Write(payload)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.URL, bytes.NewReader(payload))
	if err != nil {
		_ = d.Store.RecordWebhookDelivery(ctx, wh.ID, event, string(payload), 0, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "rss-discovery-webhook/1.0")
	req.Header.Set("X-RSS-Event", event)
	req.Header.Set("X-RSS-Timestamp", ts)
	req.Header.Set("X-RSS-Signature", sig)

	to := d.Timeout
	if to <= 0 {
		to = 10 * time.Second
	}
	client := &http.Client{
		Timeout: to,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL == nil {
				return fmt.Errorf("redirect missing url")
			}
			return security.ValidatePublicHTTPSURL(req.URL.String())
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		_ = d.Store.RecordWebhookDelivery(ctx, wh.ID, event, string(payload), 0, err.Error())
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	errMsg := ""
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errMsg = fmt.Sprintf("http %d", resp.StatusCode)
	}
	_ = d.Store.RecordWebhookDelivery(ctx, wh.ID, event, string(payload), resp.StatusCode, errMsg)
}

// Verify checks signature header against secret and body.
// Timestamp must be within MaxSkew of now to limit replay.
const MaxSkew = 5 * time.Minute

func Verify(secret, timestamp, signature string, body []byte) bool {
	if secret == "" || signature == "" || timestamp == "" {
		return false
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || ts <= 0 {
		return false
	}
	skew := time.Since(time.Unix(ts, 0).UTC())
	if skew < 0 {
		skew = -skew
	}
	if skew > MaxSkew {
		return false
	}
	const prefix = "sha256="
	if len(signature) < len(prefix) || signature[:len(prefix)] != prefix {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	want := prefix + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(signature))
}
