package httpx_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Quad4-Software/rss-discovery/internal/httpx"
)

func TestGetRespects429RetryAfter(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = io.WriteString(w, `<rss version="2.0"><channel><title>t</title></channel></rss>`)
	}))
	defer srv.Close()

	c := httpx.NewWithOptions(httpx.Options{
		Timeout:               5 * time.Second,
		MaxIdle:               8,
		UserAgent:             httpx.DefaultUA,
		MaxBytes:              1 << 20,
		ConcurrencyPerHost:    2,
		RequestsPerHostPerMin: 600,
		MinHostGap:            time.Millisecond,
		AllowPrivate:          true,
	})
	resp, err := c.Get(context.Background(), srv.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 {
		t.Fatalf("status %d", resp.Status)
	}
	if hits.Load() < 2 {
		t.Fatal("expected retry")
	}
}

func TestUserAgentHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != httpx.DefaultUA {
			t.Errorf("ua %q", r.Header.Get("User-Agent"))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := httpx.NewWithOptions(httpx.Options{
		Timeout:               2 * time.Second,
		MaxIdle:               4,
		UserAgent:             "",
		MaxBytes:              1 << 20,
		ConcurrencyPerHost:    2,
		AllowPrivate:          true,
	})
	_, err := c.Get(context.Background(), srv.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
}

func BenchmarkGetSmallBody(b *testing.B) {
	body := []byte(`<rss version="2.0"><channel><title>bench</title></channel></rss>`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := httpx.NewWithOptions(httpx.Options{
		Timeout:               5 * time.Second,
		MaxIdle:               32,
		MaxBytes:              1 << 20,
		ConcurrencyPerHost:    8,
		RequestsPerHostPerMin: 1_000_000,
		MinHostGap:            0,
		AllowPrivate:          true,
	})
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := c.Get(ctx, srv.URL+"?n="+strconv.Itoa(i), "", "")
		if err != nil {
			b.Fatal(err)
		}
		if len(resp.Body) == 0 {
			b.Fatal("empty")
		}
	}
}
