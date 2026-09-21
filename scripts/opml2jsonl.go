// Command opml2jsonl converts OPML feed lists into seed JSONL rows.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/Quad4-Software/rss-discovery/internal/opml"
)

type row struct {
	Title    string `json:"title"`
	URL      string `json:"url"`
	SiteURL  string `json:"site_url"`
	Category string `json:"category"`
	Blurb    string `json:"blurb"`
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: opml2jsonl out.jsonl in.opml...")
		os.Exit(2)
	}
	seen := map[string]struct{}{}
	out, err := os.Create(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer out.Close()
	enc := json.NewEncoder(out)
	n := 0
	for _, path := range os.Args[2:] {
		data, err := os.ReadFile(path)
		if err != nil {
			panic(err)
		}
		items, err := opml.Parse(data)
		if err != nil {
			panic(err)
		}
		for _, it := range items {
			u := strings.TrimSpace(it.XMLURL)
			if u == "" {
				continue
			}
			key := strings.ToLower(u)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			title := strings.TrimSpace(it.Title)
			if title == "" {
				title = u
			}
			r := row{
				Title:    title,
				URL:      u,
				SiteURL:  strings.TrimSpace(it.HTMLURL),
				Category: "HN Blogs",
				Blurb:    "Hacker News personal blog",
			}
			if err := enc.Encode(r); err != nil {
				panic(err)
			}
			n++
		}
	}
	fmt.Fprintf(os.Stderr, "wrote %d feeds\n", n)
}
