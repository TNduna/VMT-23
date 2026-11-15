package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// This file covers HTTP-layer error classification in doJSON.
//
// Why test this separately: each channel adapter only handles its platform's own business error codes
// (DingTalk errcode, Feishu code, Telegram ok field), while **HTTP-layer** status classification is handled uniformly by doJSON.
// The two form independent lines of defense: without HTTP-layer classification, a 503 gateway error would be treated as permanent
// and never retried, while a 403 would be retried three times in vain.

func replyServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDoJSONClassifiesHTTPStatus(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		permanent bool
	}{
		{"200 OK is not an error", 200, false},
		{"429 Too Many Requests is retryable", 429, false},
		{"408 Request Timeout is retryable", 408, false},
		{"500 Internal Server Error is retryable", 500, false},
		{"502 Bad Gateway is retryable", 502, false},
		{"503 Service Unavailable is retryable", 503, false},
		{"400 Bad Request is permanent", 400, true},
		{"401 Unauthorized is permanent", 401, true},
		{"403 Forbidden is permanent", 403, true},
		{"404 Not Found is permanent", 404, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := replyServer(t, tc.status, `{"detail":"upstream says no"}`)
			_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
			if tc.status < 300 {
				if err != nil {
					t.Fatalf("2xx should not return an error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("non-2xx should return an error")
			}
			if got := IsPermanent(err); got != tc.permanent {
				t.Fatalf("wrong permanent classification for HTTP %d: expected %v, got %v (%v)",
					tc.status, tc.permanent, got, err)
			}
			// The status code must appear in the error message so operators can tell whether it is a config error or upstream outage.
			if !strings.Contains(err.Error(), strconv.Itoa(tc.status)) {
				t.Errorf("error message should include HTTP status code %d, got %v", tc.status, err)
			}
		})
	}
}

// TestDoJSONIncludesResponseSnippet covers snippet extraction: error details returned by the upstream must be included
// so operators know why the request was rejected.
func TestDoJSONIncludesResponseSnippet(t *testing.T) {
	srv := replyServer(t, 400, `{"error":"invalid webhook token"}`)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "invalid webhook token") {
		t.Errorf("error message should include upstream description, got %v", err)
	}
}

// TestDoJSONSnippetIsSingleLineAndBounded constrains the shape of snippet output:
// upstream responses are stored in the last_error column and rendered in frontend tables, where multi-line or oversized strings would break layout.
func TestDoJSONSnippetIsSingleLineAndBounded(t *testing.T) {
	// Response containing newlines, tabs, and 5000 characters of text.
	long := strings.Repeat("x", 5000)
	srv := replyServer(t, 500, "line1\nline2\r\n\tline3 "+long)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if strings.ContainsAny(msg, "\r\n\t") {
		t.Errorf("error message should be collapsed into a single line, got %q", msg)
	}
	// snippet limit of 200 chars + fixed prefix must be far smaller than the raw response.
	if len(msg) > 400 {
		t.Errorf("error message too long (%d bytes), should be truncated by snippet: %q", len(msg), msg)
	}
}

// TestDoJSONRejectsOversizedResponse confirms response reading is bounded: if a misbehaving upstream returns a huge body,
// we must not read the entire response into memory.
func TestDoJSONRejectsOversizedResponse(t *testing.T) {
	huge := strings.Repeat("A", 1<<20) // 1 MiB
	srv := replyServer(t, 400, huge)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(err.Error()) > 400 {
		t.Errorf("oversized response should be bounded and truncated, got error length %d", len(err.Error()))
	}
}
