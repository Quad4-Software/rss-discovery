package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// runHealthcheck probes GET /healthz on the local server for container
// HEALTHCHECK use (the distroless image has no shell, curl, or wget).
// Exit 0 on HTTP 2xx, 1 otherwise.
func runHealthcheck(_ []string) int {
	addr := os.Getenv("RSS_DISCOVERY_SERVER_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8787"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: bad addr %q: %v\n", addr, err)
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}

	req, err := http.NewRequest(http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/healthz", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	req.Header.Set("User-Agent", "rss-discovery-healthcheck/1.0")

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		fmt.Fprintf(os.Stderr, "healthcheck: status %d\n", resp.StatusCode)
		return 1
	}
	return 0
}
