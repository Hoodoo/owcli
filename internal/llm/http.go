package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// APIError is a non-2xx response.
type APIError struct {
	Status  int
	Type    string
	Message string
}

func (e *APIError) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("API error %d (%s): %s", e.Status, e.Type, e.Message)
	}
	return fmt.Sprintf("API error %d: %s", e.Status, e.Message)
}

// Retryable reports statuses worth retrying: timeouts, conflicts, rate
// limits, overload, and server errors.
func (e *APIError) Retryable() bool {
	return e.Status == 408 || e.Status == 409 || e.Status == 429 || e.Status >= 500
}

// client posts JSON with retries.
type client struct {
	http       *http.Client
	maxRetries int
	backoff    func(attempt int) time.Duration
	parseError func(status int, body []byte) *APIError
}

func newClient(parseError func(int, []byte) *APIError) *client {
	return &client{
		http:       &http.Client{Timeout: 10 * time.Minute},
		maxRetries: 3,
		backoff:    func(a int) time.Duration { return time.Duration(1<<a) * time.Second },
		parseError: parseError,
	}
}

// post sends body to url and decodes a 2xx JSON response into out. Retryable
// statuses and connection errors are retried, honoring retry-after.
func (c *client) post(ctx context.Context, url string, headers map[string]string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	var last error
	for attempt := 0; ; attempt++ {
		wait, err := c.once(ctx, url, headers, payload, out)
		if err == nil {
			return nil
		}
		last = err
		if wait < 0 || attempt >= c.maxRetries || ctx.Err() != nil {
			return last
		}
		if wait == 0 {
			wait = c.backoff(attempt)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// once makes one attempt. It returns the delay before a retry: -1 when the
// error is final, 0 for the default backoff.
func (c *client) once(ctx context.Context, url string, headers map[string]string, payload []byte, out any) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return -1, err
	}
	req.Header.Set("content-type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
		return 0, fmt.Errorf("request failed: %w", err) // connection error: retry
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		apiErr := c.parseError(resp.StatusCode, data)
		if !apiErr.Retryable() {
			return -1, apiErr
		}
		return retryAfter(resp.Header.Get("retry-after")), apiErr
	}
	if err := json.Unmarshal(data, out); err != nil {
		return -1, fmt.Errorf("decode response: %w", err)
	}
	return 0, nil
}

func retryAfter(v string) time.Duration {
	if s, err := strconv.ParseFloat(v, 64); err == nil && s >= 0 {
		return time.Duration(s * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
