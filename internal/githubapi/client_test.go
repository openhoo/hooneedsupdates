package githubapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClientRetriesReplayableRequestWithinWaitBudget(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state", "rate-limit.json")
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	calls := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != `{"query":"mutation"}` {
			t.Fatalf("body=%q", body)
		}
		if calls == 1 {
			response := jsonResponse(http.StatusTooManyRequests, `{"message":"slow down"}`)
			response.Header.Set("Retry-After", "2")
			return response, nil
		}
		return jsonResponse(http.StatusOK, `{"data":{}}`), nil
	})
	client, err := New(
		&http.Client{Transport: transport}, "https://api.github.test",
		Options{StateFile: stateFile, MaxRetries: 1, MaxWait: 5 * time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return now }
	client.sleep = func(_ context.Context, delay time.Duration) error {
		now = now.Add(delay)
		return nil
	}
	request, err := http.NewRequest(
		http.MethodPost, "https://api.github.test/graphql",
		bytes.NewReader([]byte(`{"query":"mutation"}`)),
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if calls != 2 || now != time.Date(2026, 8, 31, 12, 0, 2, 0, time.UTC) {
		t.Fatalf("calls=%d now=%s", calls, now)
	}
	if data, err := os.ReadFile(stateFile); err != nil || !strings.Contains(string(data), `"active": false`) {
		t.Fatalf("successful retry did not write clear marker: %q error=%v", data, err)
	}
}

func TestClientPersistsSecondaryCooldownAndResumesLater(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "rate-limit.json")
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	limitedTransport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusForbidden, `{"message":"You have exceeded a secondary rate limit"}`), nil
	})
	client, err := New(
		&http.Client{Transport: limitedTransport}, "https://api.github.test",
		Options{StateFile: stateFile, MaxRetries: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return now }
	request, _ := http.NewRequest(http.MethodGet, "https://api.github.test/repos/openhoo/tool", nil)
	_, err = client.Do(context.Background(), request)
	var limited *RateLimitError
	if !errors.As(err, &limited) {
		t.Fatalf("error=%v", err)
	}
	if limited.Kind != "secondary" || limited.Attempts != 1 || !limited.Persisted ||
		limited.RetryAt != now.Add(time.Minute) {
		t.Fatalf("rate limit=%+v", limited)
	}
	if info, err := os.Stat(stateFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state info=%v error=%v", info, err)
	}

	resumeNow := now.Add(61 * time.Second)
	calls := 0
	resumed, err := New(
		&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return jsonResponse(http.StatusOK, `{}`), nil
		})},
		"https://api.github.test",
		Options{StateFile: stateFile, MaxRetries: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	resumed.now = func() time.Time { return resumeNow }
	response, err := resumed.Do(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if calls != 1 {
		t.Fatalf("resume calls=%d", calls)
	}
	if data, err := os.ReadFile(stateFile); err != nil || !strings.Contains(string(data), `"active": false`) {
		t.Fatalf("resume did not write clear marker: %q error=%v", data, err)
	}
	if _, err := New(http.DefaultClient, "https://api.github.test", Options{StateFile: stateFile}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stateFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restored clear marker was not consumed: %v", err)
	}
}

func TestClientContinuesExponentialSecondaryBackoffAcrossRuns(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "rate-limit.json")
	firstNow := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	limitedTransport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusForbidden, `{"message":"secondary rate limit"}`), nil
	})
	request, _ := http.NewRequest(http.MethodGet, "https://api.github.test/repos/openhoo/tool", nil)
	first, err := New(
		&http.Client{Transport: limitedTransport}, "https://api.github.test",
		Options{StateFile: stateFile},
	)
	if err != nil {
		t.Fatal(err)
	}
	first.now = func() time.Time { return firstNow }
	_, err = first.Do(context.Background(), request)
	var firstLimit *RateLimitError
	if !errors.As(err, &firstLimit) || firstLimit.Attempts != 1 {
		t.Fatalf("first error=%v rate limit=%+v", err, firstLimit)
	}

	secondNow := firstLimit.RetryAt.Add(time.Second)
	second, err := New(
		&http.Client{Transport: limitedTransport}, "https://api.github.test",
		Options{StateFile: stateFile},
	)
	if err != nil {
		t.Fatal(err)
	}
	second.now = func() time.Time { return secondNow }
	_, err = second.Do(context.Background(), request)
	var secondLimit *RateLimitError
	if !errors.As(err, &secondLimit) || secondLimit.Attempts != 2 ||
		secondLimit.RetryAt != secondNow.Add(2*time.Minute) {
		t.Fatalf("second error=%v rate limit=%+v", err, secondLimit)
	}
}

func TestClientUsesPrimaryResetAndGraphQLRateErrors(t *testing.T) {
	t.Run("primary reset", func(t *testing.T) {
		now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
		transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
			response := jsonResponse(http.StatusForbidden, `{"message":"API rate limit exceeded"}`)
			response.Header.Set("X-RateLimit-Remaining", "0")
			response.Header.Set("X-RateLimit-Reset", "1788177690")
			return response, nil
		})
		client, err := New(&http.Client{Transport: transport}, "https://api.github.test", Options{})
		if err != nil {
			t.Fatal(err)
		}
		client.now = func() time.Time { return now }
		request, _ := http.NewRequest(http.MethodGet, "https://api.github.test/rate_limit", nil)
		_, err = client.Do(context.Background(), request)
		var limited *RateLimitError
		if !errors.As(err, &limited) || limited.Kind != "primary" ||
			!limited.RetryAt.Equal(time.Unix(1788177690, 0).Add(time.Second)) {
			t.Fatalf("error=%v rate limit=%+v", err, limited)
		}
	})

	t.Run("GraphQL envelope", func(t *testing.T) {
		now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
		transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
			response := jsonResponse(http.StatusOK, `{"data":null,"errors":[{"type":"RATE_LIMITED","message":"rate limit exceeded"}]}`)
			response.Header.Set("X-RateLimit-Remaining", "0")
			response.Header.Set("X-RateLimit-Reset", "1788177690")
			return response, nil
		})
		client, err := New(&http.Client{Transport: transport}, "https://api.github.test", Options{})
		if err != nil {
			t.Fatal(err)
		}
		client.now = func() time.Time { return now }
		request, _ := http.NewRequest(http.MethodPost, "https://api.github.test/graphql", bytes.NewReader([]byte(`{}`)))
		_, err = client.Do(context.Background(), request)
		var limited *RateLimitError
		if !errors.As(err, &limited) || !limited.RetryAt.Equal(time.Unix(1788177690, 0).Add(time.Second)) {
			t.Fatalf("error=%v rate limit=%+v", err, limited)
		}
	})
}

func TestClientLeavesUnrelatedForbiddenResponseToCaller(t *testing.T) {
	client, err := New(
		&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusForbidden, `{"message":"Resource not accessible by integration"}`), nil
		})},
		"https://api.github.test", Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, "https://api.github.test/repos/openhoo/tool", nil)
	response, err := client.Do(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusForbidden || !strings.Contains(string(data), "Resource not accessible") {
		t.Fatalf("status=%d body=%q", response.StatusCode, data)
	}
}

func TestClientRejectsUnsafeStateAndEndpoint(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "state.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := New(http.DefaultClient, "https://api.github.test", Options{StateFile: link}); err == nil ||
		!strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink error=%v", err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "safe" {
		t.Fatalf("target changed: %q error=%v", data, err)
	}
	for _, endpoint := range []string{"http://api.github.test", "https://user:secret@api.github.test", "not a URL"} {
		if _, err := New(http.DefaultClient, endpoint, Options{}); err == nil {
			t.Fatalf("unsafe endpoint accepted: %q", endpoint)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
