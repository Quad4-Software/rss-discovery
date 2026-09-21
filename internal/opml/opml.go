package opml

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// Outline is a flattened feed row from OPML.
type Outline struct {
	Title       string `json:"title,omitempty"`
	XMLURL      string `json:"xml_url"`
	HTMLURL     string `json:"html_url,omitempty"`
	Category    string `json:"category,omitempty"`
	Description string `json:"description,omitempty"`
}

type document struct {
	XMLName xml.Name `xml:"opml"`
	Version string   `xml:"version,attr"`
	Head    head     `xml:"head"`
	Body    body     `xml:"body"`
}

type head struct {
	Title string `xml:"title"`
}

type body struct {
	Outlines []outlineNode `xml:"outline"`
}

type outlineNode struct {
	Text     string        `xml:"text,attr"`
	Title    string        `xml:"title,attr"`
	Type     string        `xml:"type,attr"`
	XMLURL   string        `xml:"xmlUrl,attr"`
	HTMLURL  string        `xml:"htmlUrl,attr"`
	Desc     string        `xml:"description,attr"`
	Category string        `xml:"category,attr"`
	Children []outlineNode `xml:"outline"`
}

// Parse reads OPML bytes into flattened outlines with feed URLs.
func Parse(data []byte) ([]Outline, error) {
	var doc document
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("opml: %w", err)
	}
	var out []Outline
	walk(doc.Body.Outlines, "", &out)
	return out, nil
}

// ParseReader parses from a reader.
func ParseReader(r io.Reader) ([]Outline, error) {
	data, err := io.ReadAll(io.LimitReader(r, 8<<20))
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

func walk(nodes []outlineNode, parentCat string, out *[]Outline) {
	for _, n := range nodes {
		cat := parentCat
		title := firstNonEmpty(n.Title, n.Text)
		xmlURL := strings.TrimSpace(n.XMLURL)
		if xmlURL != "" {
			c := firstNonEmpty(n.Category, cat)
			*out = append(*out, Outline{
				Title:       title,
				XMLURL:      xmlURL,
				HTMLURL:     strings.TrimSpace(n.HTMLURL),
				Category:    c,
				Description: strings.TrimSpace(n.Desc),
			})
		}
		nextCat := cat
		if xmlURL == "" && title != "" {
			if cat == "" {
				nextCat = title
			} else {
				nextCat = cat + "/" + title
			}
		}
		if len(n.Children) > 0 {
			walk(n.Children, nextCat, out)
		}
	}
}

// Export writes OPML 2.0 for outlines.
func Export(title string, items []Outline) ([]byte, error) {
	if title == "" {
		title = "RSS Discovery"
	}
	doc := document{
		Version: "2.0",
		Head:    head{Title: title},
	}
	for _, it := range items {
		if strings.TrimSpace(it.XMLURL) == "" {
			continue
		}
		doc.Body.Outlines = append(doc.Body.Outlines, outlineNode{
			Text:     firstNonEmpty(it.Title, it.XMLURL),
			Title:    it.Title,
			Type:     "rss",
			XMLURL:   it.XMLURL,
			HTMLURL:  it.HTMLURL,
			Desc:     it.Description,
			Category: it.Category,
		})
	}
	header := []byte(xml.Header)
	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(header, body...), nil
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
