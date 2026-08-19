package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Tool is one callable capability exposed to the agent loop.
type Tool struct {
	Name        string
	Description string // shown to the model in the system prompt
	Run         func(ctx context.Context, input string) (string, error)
}

// BuiltinTools returns the registry of tools available to the agent loop.
func BuiltinTools() map[string]*Tool {
	return map[string]*Tool{
		"web_search": {
			Name:        "web_search",
			Description: `web_search: search the internet. Action Input is the search query (plain text, no quotes). Returns up to 5 titles, URLs, and short snippets.`,
			Run:         webSearch,
		},
		"fetch_url": {
			Name:        "fetch_url",
			Description: `fetch_url: fetch a web page and return its main readable text. Action Input is a single URL. Use this after web_search to read a promising result in full.`,
			Run:         fetchURL,
		},
	}
}

// ── Rate limiter for web_search ──────────────────────────────────────────────

var searchMu sync.Mutex
var lastSearchTime time.Time

func throttleSearch() {
	searchMu.Lock()
	defer searchMu.Unlock()
	elapsed := time.Since(lastSearchTime)
	if elapsed < 1*time.Second {
		time.Sleep(1*time.Second - elapsed)
	}
	lastSearchTime = time.Now()
}

// ── web_search ───────────────────────────────────────────────────────────────

// DDGResult is one search result extracted from DuckDuckGo HTML.
type DDGResult struct {
	Title   string
	URL     string
	Snippet string
}

func webSearch(ctx context.Context, query string) (string, error) {
	throttleSearch()

	searchURL := fmt.Sprintf("https://html.duckduckgo.com/html/?q=%s", url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return fmt.Sprintf("search failed: %v", err), nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) llmchat-agent/1.0")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Sprintf("search failed: %v", err), nil
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024)) // 2MB max
	if err != nil {
		return fmt.Sprintf("search failed: %v", err), nil
	}
	body := string(bodyBytes)

	results := parseDDGResults(body)
	if len(results) == 0 {
		return "No search results found.", nil
	}

	var sb strings.Builder
	for i, r := range results {
		sb.WriteString(fmt.Sprintf("%d. %s\n   %s\n   %s\n\n", i+1, r.Title, r.URL, r.Snippet))
	}
	return sb.String(), nil
}

// parseDDGResults extracts search results from DuckDuckGo HTML response.
func parseDDGResults(html string) []DDGResult {
	var results []DDGResult

	// Match result links: <a rel="nofollow" class="result__a" href="...">Title</a>
	linkRe := regexp.MustCompile(`(?s)<a[^>]+class="result__a"[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	snippetRe := regexp.MustCompile(`(?s)<a[^>]+class="result__snippet"[^>]*>(.*?)</a>`)

	linkMatches := linkRe.FindAllStringSubmatch(html, -1)
	snippetMatches := snippetRe.FindAllStringSubmatch(html, -1)

	for i, lm := range linkMatches {
		if len(results) >= 5 {
			break
		}

		rawHref := lm[1]
		title := stripHTML(lm[2])

		// Extract actual URL from DDG redirect wrapper.
		actualURL := extractDDGURL(rawHref)
		if actualURL == "" {
			actualURL = rawHref
		}

		snippet := ""
		if i < len(snippetMatches) && len(snippetMatches[i]) > 1 {
			snippet = stripHTML(snippetMatches[i][1])
		}

		if title != "" {
			results = append(results, DDGResult{
				Title:   title,
				URL:     actualURL,
				Snippet: snippet,
			})
		}
	}

	return results
}

// extractDDGURL parses DDG's redirect wrapper to get the actual target URL.
// DDG wraps links as: //duckduckgo.com/l/?uddg=<url-encoded>&...
func extractDDGURL(href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	uddg := u.Query().Get("uddg")
	if uddg != "" {
		decoded, err := url.QueryUnescape(uddg)
		if err != nil {
			return uddg
		}
		return decoded
	}
	return href
}

// stripHTML removes HTML tags and unescapes common HTML entities.
func stripHTML(s string) string {
	// Remove HTML tags.
	tagRe := regexp.MustCompile(`<[^>]*>`)
	s = tagRe.ReplaceAllString(s, "")
	// Unescape common HTML entities.
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&apos;", "'")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	// Collapse whitespace.
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// ── fetch_url ────────────────────────────────────────────────────────────────

// blockedIPRanges contains private/loopback CIDRs that fetch_url must reject.
var blockedIPRanges []*net.IPNet

func init() {
	cidrs := []string{
		"127.0.0.0/8",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"::1/128",
		"fc00::/7",
		"fe80::/10",
	}
	for _, c := range cidrs {
		_, ipNet, err := net.ParseCIDR(c)
		if err == nil {
			blockedIPRanges = append(blockedIPRanges, ipNet)
		}
	}
}

func fetchURL(ctx context.Context, rawURL string) (string, error) {
	// Validate URL scheme.
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "fetch_url error: invalid URL — must start with http:// or https://", nil
	}

	// Resolve hostname and check for private/loopback IPs.
	hostname := u.Hostname()
	ips, err := net.LookupIP(hostname)
	if err != nil {
		return fmt.Sprintf("fetch_url error: cannot resolve hostname %q: %v", hostname, err), nil
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Sprintf("fetch_url error: blocked request to private/loopback address (%s resolves to %s)", hostname, ip), nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return fmt.Sprintf("fetch_url error: %v", err), nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) llmchat-agent/1.0")

	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			// Re-check redirect targets for private IPs.
			redirectHost := req.URL.Hostname()
			rip, err := net.LookupIP(redirectHost)
			if err != nil {
				return err
			}
			for _, ip := range rip {
				if isBlockedIP(ip) {
					return fmt.Errorf("redirect to blocked IP: %s", ip)
				}
			}
			return nil
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Sprintf("fetch_url error: %v", err), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("fetch_url error: HTTP %d from %s", resp.StatusCode, u.String()), nil
	}

	// Read up to 1.5MB.
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1536*1024))
	if err != nil {
		return fmt.Sprintf("fetch_url error reading body: %v", err), nil
	}

	text := extractReadableText(string(bodyBytes))

	// Truncate to ~4000 chars for small model context windows.
	const maxLen = 4000
	if utf8.RuneCountInString(text) > maxLen {
		runes := []rune(text)
		text = string(runes[:maxLen]) + "\n...[truncated]"
	}

	if strings.TrimSpace(text) == "" {
		return "fetch_url: page returned no readable text content.", nil
	}

	return text, nil
}

// isBlockedIP checks if an IP is in any of the blocked private/loopback ranges.
func isBlockedIP(ip net.IP) bool {
	for _, ipNet := range blockedIPRanges {
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

// extractReadableText strips scripts, styles, and HTML tags, then collapses whitespace.
func extractReadableText(html string) string {
	// Remove <script> and <style> blocks.
	scriptRe := regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	html = scriptRe.ReplaceAllString(html, "")
	styleRe := regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	html = styleRe.ReplaceAllString(html, "")

	// Remove HTML comments.
	commentRe := regexp.MustCompile(`(?s)<!--.*?-->`)
	html = commentRe.ReplaceAllString(html, "")

	// Remove remaining HTML tags.
	tagRe := regexp.MustCompile(`<[^>]*>`)
	html = tagRe.ReplaceAllString(html, "")

	// Unescape HTML entities.
	html = strings.ReplaceAll(html, "&amp;", "&")
	html = strings.ReplaceAll(html, "&lt;", "<")
	html = strings.ReplaceAll(html, "&gt;", ">")
	html = strings.ReplaceAll(html, "&quot;", "\"")
	html = strings.ReplaceAll(html, "&#39;", "'")
	html = strings.ReplaceAll(html, "&apos;", "'")
	html = strings.ReplaceAll(html, "&nbsp;", " ")
	html = strings.ReplaceAll(html, "\u00a0", " ")

	// Collapse whitespace.
	html = regexp.MustCompile(`[ \t]+`).ReplaceAllString(html, " ")
	html = regexp.MustCompile(`\n\s*\n+`).ReplaceAllString(html, "\n\n")
	return strings.TrimSpace(html)
}
