// Package fetch contains one Fetcher per content source. Fetchers only
// translate source formats into raw item.Items; normalization, dedupe and
// scoring happen in the pipeline.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Sahas001/prashna/internal/item"
)

// Fetcher pulls the latest items from one source. Fetchers that query
// several endpoints (feeds, authors) may return partial results together
// with an error describing what failed; the pipeline keeps the results.
type Fetcher interface {
	Name() string
	Fetch(ctx context.Context) ([]item.Item, error)
}

// Client is a small HTTP GET helper shared by fetchers: it sets the
// User-Agent, caps body size, and retries 429/5xx/transport errors with
// exponential backoff (honouring Retry-After).
type Client struct {
	HTTP       *http.Client
	UserAgent  string
	MaxRetries int
	RetryDelay time.Duration // first backoff; doubles each retry
}

func NewClient(userAgent string) *Client {
	return &Client{
		HTTP:       &http.Client{Timeout: 60 * time.Second},
		UserAgent:  userAgent,
		MaxRetries: 3,
		RetryDelay: 3 * time.Second, // arXiv asks for >=3s between requests
	}
}

const (
	maxBodyBytes  = 32 << 20
	maxRetryAfter = 2 * time.Minute
)

type statusError struct {
	code       int
	retryAfter time.Duration
}

func (e *statusError) Error() string {
	return fmt.Sprintf("unexpected status %d %s", e.code, http.StatusText(e.code))
}

// IsStatus reports whether err came from an HTTP response with this status code.
func IsStatus(err error, code int) bool {
	se, ok := errors.AsType[*statusError](err)
	return ok && se.code == code
}

func retryable(err error) bool {
	if se, ok := errors.AsType[*statusError](err); ok {
		return se.code == http.StatusTooManyRequests || se.code >= 500
	}
	return true // transport-level failure (reset, timeout, DNS blip)
}

// Response is a successful fetch. NotModified is set (and Body empty) when
// a conditional request got 304.
type Response struct {
	Body        []byte
	Header      http.Header
	NotModified bool
}

// Get fetches rawURL and returns the body of a 200 response.
func (c *Client) Get(ctx context.Context, rawURL string) ([]byte, error) {
	r, err := c.Do(ctx, rawURL, nil)
	return r.Body, err
}

// Do performs a GET with extra request headers (e.g. If-None-Match),
// retrying like Get. A 304 response is a success with NotModified set.
func (c *Client) Do(ctx context.Context, rawURL string, header http.Header) (Response, error) {
	delay := c.RetryDelay
	for attempt := 0; ; attempt++ {
		r, err := c.do(ctx, rawURL, header)
		if err == nil {
			return r, nil
		}
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		if attempt >= c.MaxRetries || !retryable(err) {
			return Response{}, fmt.Errorf("GET %s: %w", rawURL, err)
		}
		wait := delay
		if se, ok := errors.AsType[*statusError](err); ok && se.retryAfter > 0 {
			wait = min(se.retryAfter, maxRetryAfter)
		}
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case <-time.After(wait):
		}
		delay *= 2
	}
}

func (c *Client) do(ctx context.Context, rawURL string, header http.Header) (Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Response{}, err
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified && len(header) > 0 {
		return Response{Header: resp.Header, NotModified: true}, nil
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		se := &statusError{code: resp.StatusCode}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			se.retryAfter = time.Duration(s) * time.Second
		}
		return Response{}, se
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return Response{}, err
	}
	if len(body) > maxBodyBytes {
		return Response{}, fmt.Errorf("response exceeds %d bytes", maxBodyBytes)
	}
	return Response{Body: body, Header: resp.Header}, nil
}
