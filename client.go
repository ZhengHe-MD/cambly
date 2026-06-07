// Package cambly is a small, dependency-free client for Cambly's private web
// API (reverse-engineered from the student web app). It is consumed by the
// `cambly` CLI but is usable as a library on its own.
//
// Authentication is the signed Flask `session` cookie that the web app stores
// after login; writes additionally require the `csrfToken` cookie echoed in an
// `x-csrf` header. The client respects HTTP(S)_PROXY environment variables.
package cambly

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the origin every endpoint hangs off of.
const DefaultBaseURL = "https://www.cambly.com"

// defaultUserAgent mirrors a normal Chrome request so Cloudflare stays happy.
const defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36"

// ErrNotAuthenticated is returned when the session cookie is missing, expired,
// or rejected by the server (the API answers {"result": null} in that case).
var ErrNotAuthenticated = errors.New("not authenticated: session is missing or expired (run `cambly login`)")

// Client talks to the Cambly web API.
type Client struct {
	BaseURL   string
	UserAgent string
	Session   string // value of the `session` cookie
	CSRF      string // value of the `csrfToken` cookie (required for writes)
	HTTP      *http.Client

	meID string // cached id of the authenticated user
}

// Config configures a Client.
type Config struct {
	Session   string
	CSRF      string
	BaseURL   string        // defaults to DefaultBaseURL
	UserAgent string        // defaults to a Chrome UA
	Timeout   time.Duration // defaults to 30s
}

// New builds a Client. The HTTP transport uses http.ProxyFromEnvironment so it
// honours the caller's HTTP(S)_PROXY settings (Cambly is often reached via a
// proxy).
func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = defaultUserAgent
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &Client{
		BaseURL:   strings.TrimRight(cfg.BaseURL, "/"),
		UserAgent: cfg.UserAgent,
		Session:   cfg.Session,
		CSRF:      cfg.CSRF,
		HTTP:      &http.Client{Timeout: cfg.Timeout, Transport: transport},
	}
}

// APIError is returned for non-2xx responses, preserving status and body.
type APIError struct {
	Status int
	Method string
	Path   string
	Body   string
}

func (e *APIError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 300 {
		body = body[:300] + "…"
	}
	return fmt.Sprintf("cambly api %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, body)
}

// do performs a request and returns the raw response body, mapping non-2xx to
// an *APIError.
func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any) ([]byte, error) {
	if c.Session == "" {
		return nil, ErrNotAuthenticated
	}
	u := c.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}

	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
		reqBody = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return nil, err
	}

	cookie := "session=" + c.Session
	if c.CSRF != "" {
		cookie += "; csrfToken=" + c.CSRF
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.CSRF != "" && method != http.MethodGet {
		req.Header.Set("x-csrf", c.CSRF)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{Status: resp.StatusCode, Method: method, Path: path, Body: string(data)}
	}
	return data, nil
}

// result is the common {"result": …} envelope most /api endpoints use.
type envelope struct {
	Result json.RawMessage `json:"result"`
}

// getResult performs a GET and unwraps {"result": <T>} into out. A null result
// is reported as ErrNotAuthenticated for endpoints that require a session.
func getResult[T any](ctx context.Context, c *Client, path string, q url.Values, requireAuth bool) (T, error) {
	var zero T
	data, err := c.do(ctx, http.MethodGet, path, q, nil)
	if err != nil {
		return zero, err
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return zero, fmt.Errorf("decode %s: %w", path, err)
	}
	if requireAuth && (len(env.Result) == 0 || string(bytes.TrimSpace(env.Result)) == "null") {
		return zero, ErrNotAuthenticated
	}
	var out T
	if len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, &out); err != nil {
			return zero, fmt.Errorf("decode %s result: %w", path, err)
		}
	}
	return out, nil
}

// postResult performs a write and unwraps {"result": <T>} into out.
func postResult[T any](ctx context.Context, c *Client, path string, q url.Values, body any) (T, error) {
	var zero T
	data, err := c.do(ctx, http.MethodPost, path, q, body)
	if err != nil {
		return zero, err
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return zero, fmt.Errorf("decode %s: %w", path, err)
	}
	var out T
	if len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, &out); err != nil {
			return zero, fmt.Errorf("decode %s result: %w", path, err)
		}
	}
	return out, nil
}

// getRaw performs a GET against a non-/api endpoint that does NOT use the
// {"result": …} envelope (e.g. /getTutorSchedule) and decodes the top-level
// payload into out.
func getRaw[T any](ctx context.Context, c *Client, path string, q url.Values) (T, error) {
	var out T
	data, err := c.do(ctx, http.MethodGet, path, q, nil)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, fmt.Errorf("decode %s: %w", path, err)
	}
	return out, nil
}
