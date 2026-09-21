package extract

import (
	"bytes"
	nurl "net/url"
	"strings"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
)

// Article is extracted readable content.
type Article struct {
	Title    string
	Text     string
	Excerpt  string
	Byline   string
	SiteName string
	ImageURL string
	Length   int
}

// FromURL fetches and extracts readable content.
func FromURL(pageURL string, timeout time.Duration) (*Article, error) {
	a, err := readability.FromURL(pageURL, timeout)
	if err != nil {
		return nil, err
	}
	return mapArticle(a), nil
}

// FromHTML extracts from an HTML body.
func FromHTML(html string, pageURL string) (*Article, error) {
	u, err := nurl.Parse(pageURL)
	if err != nil {
		return nil, err
	}
	a, err := readability.FromReader(strings.NewReader(html), u)
	if err != nil {
		return nil, err
	}
	return mapArticle(a), nil
}

func mapArticle(a readability.Article) *Article {
	var buf bytes.Buffer
	_ = a.RenderText(&buf)
	text := strings.TrimSpace(buf.String())
	excerpt := strings.TrimSpace(a.Excerpt())
	if excerpt == "" && text != "" {
		excerpt = truncate(text, 280)
	}
	return &Article{
		Title:    strings.TrimSpace(a.Title()),
		Text:     text,
		Excerpt:  excerpt,
		Byline:   strings.TrimSpace(a.Byline()),
		SiteName: strings.TrimSpace(a.SiteName()),
		ImageURL: strings.TrimSpace(a.ImageURL()),
		Length:   len(text),
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
