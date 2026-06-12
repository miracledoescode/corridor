package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

const (
	// maxBodyBytes caps how much of a venue response we will buffer.
	// WHY: a misbehaving (or compromised) venue endpoint must not be able
	// to OOM the one process that may never go down.
	maxBodyBytes = 32 << 20 // 32 MiB

	errSnippetLen = 200
)

// HTTPError is a non-2xx venue response: status plus a truncated body
// snippet, safe to log (no headers, no credentials, bounded size).
type HTTPError struct {
	Status  int
	URL     string
	Snippet string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("http %d from %s: %s", e.Status, e.URL, e.Snippet)
}

// Client is the hardened HTTP client every adapter shares the shape of:
// identified User-Agent, explicit timeouts, a hard per-venue rate cap,
// Content-Type validation, and bounded reads.
type Client struct {
	hc      *http.Client
	ua      string
	limiter *rate.Limiter
}

// NewClient builds a client with the politeness/security defaults from the
// brief: 5s connect timeout, 30s overall timeout, rps requests/second hard
// cap shared across every endpoint of one venue.
func NewClient(userAgent string, rps float64) *Client {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 5 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
	}
	return &Client{
		hc: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
		ua:      userAgent,
		limiter: rate.NewLimiter(rate.Limit(rps), 1),
	}
}

// GetJSON fetches url and decodes the JSON response into out.
func (c *Client) GetJSON(ctx context.Context, url string, out any) error {
	return c.doJSON(ctx, http.MethodGet, url, nil, out)
}

// PostJSON sends body as JSON to url and decodes the response into out.
func (c *Client) PostJSON(ctx context.Context, url string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	return c.doJSON(ctx, http.MethodPost, url, bytes.NewReader(b), out)
}

func (c *Client) doJSON(ctx context.Context, method, url string, body io.Reader, out any) error {
	// The limiter is the hard rate cap: every request to this venue,
	// regardless of endpoint or goroutine, waits its turn here.
	if err := c.limiter.Wait(ctx); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errSnippetLen))
		return &HTTPError{Status: resp.StatusCode, URL: url, Snippet: string(snippet)}
	}

	// WHY validate Content-Type: a captive portal, proxy error page, or
	// hijacked DNS answer returns HTML with status 200; parsing it as JSON
	// would store garbage in the moat.
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "json") {
		return fmt.Errorf("unexpected content-type %q from %s", ct, url)
	}

	dec := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes))
	// WHY UseNumber: keeps numeric JSON values as exact decimal text
	// (json.Number) instead of float64 — the never-float rule starts at
	// the decoder, not the database.
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", url, err)
	}
	return nil
}

// UserAgent builds the identified UA string required on every venue request.
func UserAgent(contact string) string {
	return fmt.Sprintf("CorridorBot/0.1 (contact: %s)", contact)
}
