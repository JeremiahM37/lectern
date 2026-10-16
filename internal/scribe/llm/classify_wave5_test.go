package llm

import (
	"errors"
	"fmt"
	"testing"
)

// A 403 whose body says the service is rate limited or overloaded is retried
// like a 429. A plain 403 stays an auth error.
func TestClassify403WithRateOrOverloadBodyIsTransient(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		want  ErrorCategory
		retry bool
	}{
		{"403 rate body", &APIError{StatusCode: 403, Message: "Rate limit exceeded for key"}, CatRateLimited, true},
		{"403 overloaded body", &APIError{StatusCode: 403, Message: "upstream is overloaded"}, CatOverloaded, true},
		{"403 auth body", &APIError{StatusCode: 403, Message: "forbidden: key lacks scope"}, CatAuth, false},
		{"401 stays auth", &APIError{StatusCode: 401, Message: "rate"}, CatAuth, false},
	}
	for _, tc := range cases {
		cat, retry := Classify(tc.err)
		if cat != tc.want || retry != tc.retry {
			t.Errorf("%s: got %v/%v, want %v/%v", tc.name, cat, retry, tc.want, tc.retry)
		}
	}
}

// Network failures the older list missed are retried.
func TestClassifyTransientNetworkMessages(t *testing.T) {
	for _, msg := range []string{
		"getaddrinfo ENOTFOUND api.example.com",
		"EAI_AGAIN temporary failure in name resolution",
		"stream ended: premature close",
		"socket hang up",
		"read tcp: connection reset by peer",
		"connection aborted",
		"request terminated",
	} {
		cat, retry := Classify(fmt.Errorf("wrapped: %s", msg))
		if cat != CatNetwork || !retry {
			t.Errorf("%q: got %v/%v, want network/retry", msg, cat, retry)
		}
	}
}

// The new patterns do not catch ordinary text that happens to contain
// "connection" or a similar word.
func TestClassifyNewPatternsAreNarrow(t *testing.T) {
	for _, msg := range []string{
		"invalid connection string in config",
		"unknown model: terminator-1",
		"this field is not supported by the terminator model",
	} {
		if cat, _ := Classify(errors.New(msg)); cat == CatNetwork {
			t.Errorf("%q must not be classed as a network error", msg)
		}
	}
}
