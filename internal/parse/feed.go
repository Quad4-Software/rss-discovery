package parse

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mmcdole/gofeed"
	"github.com/Quad4-Software/rss-discovery/internal/store"
)

var parserPool = sync.Pool{
	New: func() any { return gofeed.NewParser() },
}

// Result is a normalized feed parse.
type Result struct {
	Title       string
	Description string
	Link        string
	Language    string
	Format      string
	ImageURL    string
	Entries     []store.Entry
}

// Feed parses RSS, Atom, or JSON Feed from body.
func Feed(body []byte, feedID string) (*Result, error) {
	parser := parserPool.Get().(*gofeed.Parser)
	defer parserPool.Put(parser)
	fp, err := parser.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	format := detectFormat(body, fp)
	out := &Result{
		Title:       strings.TrimSpace(fp.Title),
		Description: strings.TrimSpace(fp.Description),
		Link:        strings.TrimSpace(fp.Link),
		Language:    strings.TrimSpace(fp.Language),
		Format:      format,
	}
	if fp.Image != nil {
		out.ImageURL = fp.Image.URL
	}
	if n := len(fp.Items); n > 0 {
		out.Entries = make([]store.Entry, 0, n)
	}
	for _, item := range fp.Items {
		e := store.Entry{
			FeedID:  feedID,
			GUID:    firstNonEmpty(item.GUID, item.Link),
			URL:     strings.TrimSpace(item.Link),
			Title:   strings.TrimSpace(item.Title),
			Summary: firstNonEmpty(stripHTML(item.Description), stripHTML(item.Content)),
			Author:  authorName(item),
		}
		if item.PublishedParsed != nil {
			t := item.PublishedParsed.UTC()
			e.PublishedAt = &t
		} else if item.UpdatedParsed != nil {
			t := item.UpdatedParsed.UTC()
			e.PublishedAt = &t
		}
		if item.UpdatedParsed != nil {
			t := item.UpdatedParsed.UTC()
			e.UpdatedAt = &t
		}
		if item.Image != nil && item.Image.URL != "" {
			e.ImageURL = item.Image.URL
		} else if len(item.Enclosures) > 0 {
			for _, enc := range item.Enclosures {
				if enc != nil && strings.HasPrefix(enc.Type, "image/") && enc.URL != "" {
					e.ImageURL = enc.URL
					break
				}
			}
		}
		sum := sha256.Sum256([]byte(e.Title + "|" + e.Summary + "|" + e.URL))
		e.ContentHash = hex.EncodeToString(sum[:16])
		e.ID = store.EntryID(feedID, e.GUID, e.URL)
		out.Entries = append(out.Entries, e)
	}
	return out, nil
}

func detectFormat(body []byte, fp *gofeed.Feed) string {
	if fp != nil && fp.FeedType != "" {
		switch strings.ToLower(fp.FeedType) {
		case "rss":
			return "RSS"
		case "atom":
			return "Atom"
		case "json":
			return "JSON"
		}
	}
	trim := bytes.TrimSpace(body)
	if len(trim) == 0 {
		return "UNKNOWN"
	}
	if trim[0] == '{' {
		return "JSON"
	}
	head := trim
	if len(head) > 200 {
		head = head[:200]
	}
	lower := bytes.ToLower(head)
	if bytes.Contains(lower, []byte("<feed")) {
		return "Atom"
	}
	if bytes.Contains(lower, []byte("<rss")) {
		return "RSS"
	}
	return "UNKNOWN"
}

func authorName(item *gofeed.Item) string {
	if item.Author != nil && item.Author.Name != "" {
		return item.Author.Name
	}
	if len(item.Authors) > 0 && item.Authors[0].Name != "" {
		return item.Authors[0].Name
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}

func stripHTML(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	inTag := false
	space := false
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case inTag:
		case unicode.IsSpace(r):
			space = true
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		}
	}
	return b.String()
}

// LimitedReader caps body size.
func LimitedReader(r io.Reader, max int64) io.Reader {
	return io.LimitReader(r, max)
}

// NowUTC helper for tests.
func NowUTC() time.Time { return time.Now().UTC() }
