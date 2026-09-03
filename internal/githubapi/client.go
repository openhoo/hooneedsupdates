package githubapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	stateVersion       = 1
	inspectionLimit    = 64 << 10
	secondaryBaseDelay = time.Minute
	secondaryMaxDelay  = time.Hour
)

// Options controls bounded retries and optional cooldown persistence. MaxWait
// is the total time one request may spend waiting between attempts; longer
// cooldowns are returned to the caller as RateLimitError.
type Options struct {
	StateFile  string
	MaxRetries int
	MaxWait    time.Duration
}

// RateLimitError means GitHub rejected or deferred the request until RetryAt.
// Callers can safely stop a fleet run without treating dependencies as
// unresolved and resume after the persisted cooldown.
type RateLimitError struct {
	Kind      string
	RetryAt   time.Time
	Attempts  int
	Persisted bool
}

func (e *RateLimitError) Error() string {
	location := "in memory"
	if e.Persisted {
		location = "in persistent state"
	}
	return fmt.Sprintf(
		"GitHub %s rate limit; retry after %s (%s)",
		e.Kind, e.RetryAt.UTC().Format(time.RFC3339), location,
	)
}

type cooldownState struct {
	Version   int       `json:"version"`
	Host      string    `json:"host"`
	Active    bool      `json:"active"`
	Kind      string    `json:"kind"`
	RetryAt   time.Time `json:"retryAt"`
	Attempts  int       `json:"attempts"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Client applies GitHub's primary and secondary rate-limit guidance to REST
// and GraphQL calls. Calls are serialized to prevent a post-limit thundering
// herd while other registry traffic remains concurrent.
type Client struct {
	http       *http.Client
	host       string
	stateFile  string
	maxRetries int
	maxWait    time.Duration

	mu    sync.Mutex
	state *cooldownState
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

func New(client *http.Client, apiURL string, options Options) (*Client, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	parsed, err := url.Parse(apiURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("GitHub API URL must be an HTTPS URL without credentials")
	}
	if options.MaxRetries < 0 {
		return nil, errors.New("GitHub max retries must not be negative")
	}
	if options.MaxWait < 0 {
		return nil, errors.New("GitHub max wait must not be negative")
	}
	result := &Client{
		http: client, host: strings.ToLower(parsed.Host),
		stateFile: options.StateFile, maxRetries: options.MaxRetries, maxWait: options.MaxWait,
		now: time.Now,
		sleep: func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	if err := result.loadState(); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, errors.New("GitHub request and URL are required")
	}
	if !strings.EqualFold(request.URL.Host, c.host) {
		return nil, fmt.Errorf("refusing GitHub request to unexpected host %q", request.URL.Host)
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	waitBudget := c.maxWait
	retries := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if c.state != nil && c.state.RetryAt.After(c.now()) {
			delay := c.state.RetryAt.Sub(c.now())
			if delay > waitBudget {
				return nil, c.rateLimitError()
			}
			if err := c.sleep(ctx, delay); err != nil {
				return nil, err
			}
			waitBudget -= delay
		}

		attempt, err := cloneRequest(ctx, request)
		if err != nil {
			return nil, err
		}
		response, err := c.http.Do(attempt)
		if err != nil {
			return nil, err
		}
		limited, kind, retryAt, inspectErr := c.inspect(response)
		if inspectErr != nil {
			_ = response.Body.Close()
			return nil, inspectErr
		}
		if !limited {
			c.clearStateBestEffort()
			return response, nil
		}
		_ = response.Body.Close()
		if err := c.record(kind, retryAt); err != nil {
			return nil, err
		}
		if retries >= c.maxRetries {
			return nil, c.rateLimitError()
		}
		delay := c.state.RetryAt.Sub(c.now())
		if delay < 0 {
			delay = 0
		}
		if delay > waitBudget {
			return nil, c.rateLimitError()
		}
		retries++
	}
}

func cloneRequest(ctx context.Context, request *http.Request) (*http.Request, error) {
	clone := request.Clone(ctx)
	if request.Body == nil {
		return clone, nil
	}
	if request.GetBody == nil {
		return nil, errors.New("GitHub request body cannot be replayed safely")
	}
	body, err := request.GetBody()
	if err != nil {
		return nil, fmt.Errorf("recreate GitHub request body: %w", err)
	}
	clone.Body = body
	return clone, nil
}

func (c *Client) inspect(response *http.Response) (bool, string, time.Time, error) {
	data, complete, err := inspectBody(response)
	if err != nil {
		return false, "", time.Time{}, err
	}
	remaining := strings.TrimSpace(response.Header.Get("X-RateLimit-Remaining"))
	retryHeader := strings.TrimSpace(response.Header.Get("Retry-After"))
	message := strings.ToLower(githubMessage(data))
	primary := remaining == "0" && (response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests)
	secondary := response.StatusCode == http.StatusTooManyRequests ||
		(response.StatusCode == http.StatusForbidden && (retryHeader != "" ||
			strings.Contains(message, "secondary rate limit") || strings.Contains(message, "abuse detection")))
	graphql := response.StatusCode >= 200 && response.StatusCode < 300 && complete && graphqlRateLimited(data)
	if !primary && !secondary && !graphql {
		return false, "", time.Time{}, nil
	}

	kind := "secondary"
	if primary || (graphql && remaining == "0") {
		kind = "primary"
	}
	now := c.now()
	if retryAt, ok := parseRetryAfter(retryHeader, now); ok {
		return true, kind, retryAt, nil
	}
	if primary || graphql {
		if reset, ok := parseReset(response.Header.Get("X-RateLimit-Reset"), now); ok {
			return true, kind, reset, nil
		}
	}
	attempts := 1
	if c.state != nil && c.state.Kind == kind {
		attempts = c.state.Attempts + 1
	}
	return true, kind, now.Add(secondaryDelay(attempts)), nil
}

func inspectBody(response *http.Response) ([]byte, bool, error) {
	if response.Body == nil {
		return nil, true, nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, inspectionLimit+1))
	if err != nil {
		return nil, false, fmt.Errorf("inspect GitHub response: %w", err)
	}
	complete := len(data) <= inspectionLimit
	response.Body = &replayBody{
		Reader: io.MultiReader(bytes.NewReader(data), response.Body),
		Closer: response.Body,
	}
	return data, complete, nil
}

type replayBody struct {
	io.Reader
	io.Closer
}

func githubMessage(data []byte) string {
	var result struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &result)
	return result.Message
}

func graphqlRateLimited(data []byte) bool {
	var result struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Type       string `json:"type"`
			Message    string `json:"message"`
			Extensions struct {
				Type string `json:"type"`
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if json.Unmarshal(data, &result) != nil || len(result.Errors) == 0 {
		return false
	}
	if len(result.Data) > 0 && string(result.Data) != "null" && string(result.Data) != "{}" {
		return false
	}
	for _, entry := range result.Errors {
		values := []string{entry.Type, entry.Extensions.Type, entry.Extensions.Code, entry.Message}
		matched := false
		for _, value := range values {
			normalized := strings.ToLower(value)
			if strings.Contains(normalized, "rate_limit") || strings.Contains(normalized, "rate limit") {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func parseRetryAfter(value string, now time.Time) (time.Time, bool) {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && seconds >= 0 {
		if seconds > math.MaxInt64/int64(time.Second) {
			seconds = math.MaxInt64 / int64(time.Second)
		}
		return now.Add(time.Duration(seconds) * time.Second), true
	}
	if parsed, err := http.ParseTime(value); err == nil {
		if parsed.Before(now) {
			return now, true
		}
		return parsed, true
	}
	return time.Time{}, false
}

func parseReset(value string, now time.Time) (time.Time, bool) {
	seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || seconds < 0 {
		return time.Time{}, false
	}
	reset := time.Unix(seconds, 0).Add(time.Second)
	if reset.Before(now) {
		reset = now
	}
	return reset, true
}

func secondaryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := secondaryBaseDelay
	for attempt := 1; attempt < attempts && delay < secondaryMaxDelay; attempt++ {
		delay *= 2
		if delay > secondaryMaxDelay {
			delay = secondaryMaxDelay
		}
	}
	return delay
}

func (c *Client) record(kind string, retryAt time.Time) error {
	attempts := 1
	if c.state != nil && c.state.Kind == kind {
		attempts = c.state.Attempts + 1
	}
	updatedAt := c.now().UTC()
	if retryAt.Before(updatedAt) {
		retryAt = updatedAt
	}
	c.state = &cooldownState{
		Version: stateVersion, Host: c.host, Active: true, Kind: kind,
		RetryAt: retryAt.UTC(), Attempts: attempts, UpdatedAt: updatedAt,
	}
	return c.saveState(c.state)
}

func (c *Client) rateLimitError() *RateLimitError {
	return &RateLimitError{
		Kind: c.state.Kind, RetryAt: c.state.RetryAt.UTC(), Attempts: c.state.Attempts,
		Persisted: c.stateFile != "",
	}
}

func (c *Client) loadState() error {
	if c.stateFile == "" {
		return nil
	}
	info, err := os.Lstat(c.stateFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect GitHub rate-limit state: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("GitHub rate-limit state must be a regular file")
	}
	data, err := os.ReadFile(c.stateFile)
	if err != nil {
		return fmt.Errorf("read GitHub rate-limit state: %w", err)
	}
	var state cooldownState
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return fmt.Errorf("decode GitHub rate-limit state: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("decode GitHub rate-limit state: trailing data")
	}
	if state.Version != stateVersion || state.UpdatedAt.IsZero() {
		return errors.New("GitHub rate-limit state is invalid")
	}
	if !strings.EqualFold(state.Host, c.host) {
		if err := os.Remove(c.stateFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove GitHub rate-limit state for another host: %w", err)
		}
		return nil
	}
	if !state.Active {
		if err := os.Remove(c.stateFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove cleared GitHub rate-limit state: %w", err)
		}
		return nil
	}
	if (state.Kind != "primary" && state.Kind != "secondary") || state.Attempts < 1 ||
		state.RetryAt.IsZero() || state.RetryAt.Before(state.UpdatedAt) {
		return errors.New("GitHub rate-limit state is invalid")
	}
	c.state = &state
	return nil
}

func (c *Client) saveState(state *cooldownState) error {
	if c.stateFile == "" {
		return nil
	}
	directory := filepath.Dir(c.stateFile)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create GitHub rate-limit state directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode GitHub rate-limit state: %w", err)
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(directory, ".rate-limit-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary GitHub rate-limit state: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure temporary GitHub rate-limit state: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary GitHub rate-limit state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary GitHub rate-limit state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary GitHub rate-limit state: %w", err)
	}
	if err := os.Rename(temporaryName, c.stateFile); err != nil {
		return fmt.Errorf("replace GitHub rate-limit state: %w", err)
	}
	return nil
}

func (c *Client) clearStateBestEffort() {
	if c.state == nil {
		return
	}
	c.state = nil
	_ = c.saveState(&cooldownState{
		Version: stateVersion, Host: c.host, Active: false, UpdatedAt: c.now().UTC(),
	})
}
