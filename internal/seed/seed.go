package seed

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/Quad4-Software/rss-discovery/internal/store"
)

type seedFeed struct {
	Title    string `json:"title"`
	URL      string `json:"url"`
	SiteURL  string `json:"site_url"`
	Category string `json:"category"`
	Blurb    string `json:"blurb"`
}

// LoadDir imports catalog.jsonl, extras.jsonl, hn-blogs.jsonl, and kagi-smallweb.txt into the catalog.
func LoadDir(ctx context.Context, st *store.Store, dir string) (int, error) {
	total := 0
	for _, name := range []string{"catalog.jsonl", "extras.jsonl", "hn-blogs.jsonl"} {
		path := filepath.Join(dir, name)
		n, err := loadJSONL(ctx, st, path, name)
		if err != nil && !os.IsNotExist(err) {
			return total, err
		}
		total += n
		slog.Info("seed jsonl", "file", name, "n", n)
	}
	sw := filepath.Join(dir, "kagi-smallweb.txt")
	n, err := loadSmallWeb(ctx, st, sw)
	if err != nil && !os.IsNotExist(err) {
		return total, err
	}
	total += n
	slog.Info("seed smallweb", "n", n)
	_ = st.SetMeta(ctx, "seeded_at", fmt.Sprintf("%d", total))
	return total, nil
}

func loadJSONL(ctx context.Context, st *store.Store, path, source string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var batch []store.Feed
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var sf seedFeed
		if err := json.Unmarshal([]byte(line), &sf); err != nil {
			continue
		}
		if sf.URL == "" {
			continue
		}
		batch = append(batch, store.Feed{
			ID:       store.FeedID(sf.URL),
			URL:      sf.URL,
			SiteURL:  sf.SiteURL,
			Title:    sf.Title,
			Category: sf.Category,
			Blurb:    sf.Blurb,
			Source:   source,
		})
		if len(batch) >= 500 {
			n, err := st.UpsertFeedsBatch(ctx, batch)
			if err != nil {
				return 0, err
			}
			_ = n
			batch = batch[:0]
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	if len(batch) > 0 {
		if _, err := st.UpsertFeedsBatch(ctx, batch); err != nil {
			return 0, err
		}
	}
	count, _ := countSource(ctx, st, source)
	return int(count), nil
}

func loadSmallWeb(ctx context.Context, st *store.Store, path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var batch []store.Feed
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			continue
		}
		title := u.Hostname()
		site := u.Scheme + "://" + u.Host
		batch = append(batch, store.Feed{
			ID:       store.FeedID(raw),
			URL:      raw,
			SiteURL:  site,
			Title:    title,
			Category: "Blogs",
			Blurb:    "Kagi Small Web",
			Source:   "kagi-smallweb",
		})
		if len(batch) >= 1000 {
			if _, err := st.UpsertFeedsBatch(ctx, batch); err != nil {
				return 0, err
			}
			batch = batch[:0]
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	if len(batch) > 0 {
		if _, err := st.UpsertFeedsBatch(ctx, batch); err != nil {
			return 0, err
		}
	}
	count, _ := countSource(ctx, st, "kagi-smallweb")
	return int(count), nil
}

func countSource(ctx context.Context, st *store.Store, source string) (int64, error) {
	var n int64
	err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM feeds WHERE source=?`, source).Scan(&n)
	return n, err
}
