package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// allowLocalTargets determines whether messages may be delivered to loopback / link-local addresses.
//
// Denied by default. These address ranges are never used by public IM bots or public mail servers, yet what they can reach
// is highly sensitive: admin ports of co-located services on the same host, or cloud metadata endpoints
// (169.254.169.254, which exposes instance credentials). Although delivery URLs are configured by admins, an admin session
// hijacked via XSS/CSRF or a second user sharing the same JWT could modify the configuration to read back response content—
// doJSON writes the first 200 bytes of 4xx/5xx response bodies into last_error, which the delivery history API echoes back,
// forming a semi-blind read primitive.
//
// However, a "local SMTP relay" (postfix on 127.0.0.1:25) is a common setup for self-hosted email,
// so a blanket ban would block legitimate deployments. We provide an explicit escape hatch rather than hardcoding an exception:
// set ARTEX_NOTIFY_ALLOW_LOCAL=1 to allow it.
//
// Exported as AllowLocalTargetsEnv so tests can explicitly enable it—test cases in this package and the server package
// heavily use httptest mock servers on 127.0.0.1, which would otherwise be blocked by the dial guard.
const AllowLocalTargetsEnv = "VMT23_NOTIFY_ALLOW_LOCAL"
const LegacyAllowLocalTargetsEnv = "ARTEX_NOTIFY_ALLOW_LOCAL"

func allowLocalTargets() bool {
	v := strings.TrimSpace(os.Getenv(AllowLocalTargetsEnv))
	if v == "" {
		v = strings.TrimSpace(os.Getenv(LegacyAllowLocalTargetsEnv))
	}
	return v == "1" || strings.EqualFold(v, "true")
}

// isBlockedDialIP reports whether the target IP belongs to an address range disallowed for delivery by default.
//
// Rejects only loopback, link-local (including cloud metadata 169.254.169.254), unspecified, and multicast addresses.
// **Does not reject** RFC1918 private networks: self-hosted Mattermost / SMTP relays on internal networks are common legitimate use cases,
// and blocking them would make the feature unusable in real enterprise environments. This trade-off is deliberate—
// the guard must block truly sensitive targets without breaking normal deployments.
func isBlockedDialIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// IPv4-mapped IPv6 (::ffff:127.0.0.1) must be unmapped to IPv4 before checking, or it would bypass the check.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// blockInternalDial is the Control hook on http.Transport's dialer, checking the target address **when establishing a connection**.
//
// Why enforce this at dial time rather than only during config validation: dial time is the final enforcement point.
// It covers two scenarios that bypass config-time validation—DNS rebinding (resolving to a public IP during validation
// but to an internal IP when actually connecting) and redirects (even though cross-host redirects are rejected, same-host redirects
// can still point to a different path).
func blockInternalDial(_, address string, _ syscall.RawConn) error {
	if allowLocalTargets() {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("cannot resolve target address %q", host)
	}
	if isBlockedDialIP(ip) {
		return fmt.Errorf("delivery to loopback/link-local address %s is denied (set %s=1 if local service delivery is required)", ip, AllowLocalTargetsEnv)
	}
	return nil
}

// notifyTransport adds only the dial guard on top of the default Transport.
// Uses Clone to preserve all default tuning (connection pooling, HTTP/2, timeouts, proxy, etc.),
// avoiding unintended behavioral changes just to add a single check.
var notifyTransport = func() *http.Transport {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{}
	}
	clone := t.Clone()
	clone.DialContext = (&net.Dialer{Timeout: 10 * time.Second, Control: blockInternalDial}).DialContext
	return clone
}()

// httpClient is the shared HTTP client for all channel deliveries.
//
// Intentionally **does not** reuse the project's global outbound proxy (GlobalProxy on the server side): that proxy is meant
// for penetration-testing target traffic and is often an unstable tunnel, whereas notification availability should not be held hostage
// by target network jitter. IM pushes connect directly. Timeout is set to 15 seconds—any upstream slower than that is effectively down.
//
// Cross-host redirects are rejected: delivery URLs for these channels are fixed endpoints that never legitimately redirect
// to another host; moreover, credentials for several channels (DingTalk access_token, WeCom key, Telegram bot token)
// **live right in the URL**, so following a cross-host redirect would hand those credentials over to the redirect target.
// Same-host redirects (such as adding a trailing slash) are still allowed.
var httpClient = &http.Client{
	Timeout:   15 * time.Second,
	Transport: notifyTransport,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("cross-host redirect denied (%s → %s)", via[0].URL.Host, req.URL.Host)
		}
		return nil
	},
}

// respBodyLimit caps the response body size read into memory. Misbehaving upstreams may dump huge responses, whereas we only need
// the error code and a short error description to display in the delivery history.
const respBodyLimit = 8 << 10

// doJSON sends a single HTTP request and returns the (size-limited) response body.
//
// When payload is nil, an empty body is sent (for GET requests or platforms that do not expect a body).
// Key-value pairs in headers are attached as-is for custom headers in generic Webhooks.
//
// Error classification is the core responsibility of this function: network-layer failures and 5xx/408/429 responses are classified as "retryable",
// while other 4xx responses are classified as "permanent failures"—retrying a 403 just spams the same error 3 times in the logs.
func doJSON(ctx context.Context, method, url string, headers map[string]string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			// Serialization failure is a local bug (wrong config field type); retrying will not fix it.
			return nil, Permanent(fmt.Errorf("failed to build request body: %w", err))
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		// Invalid URL—most likely a user typo in the address, which is a permanent failure.
		// Never pass err through directly: url.Parse error strings include the full URL.
		return nil, Permanent(fmt.Errorf("invalid request URL: %s", redactRequestTarget(url)))
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		// Connection refused, DNS failure, timeout—mostly transient faults, handed over to backoff retry.
		//
		// The error text must be redacted before returning. Reason: http.Client.Do returns *url.Error,
		// whose Error() formats as `Op "full-URL": underlying error`, and credentials for these channels **live in the URL**
		// (DingTalk access_token, WeCom key, Feishu hook id, Telegram /bot<token>/).
		// Without redaction, credentials would leak through this error string into four places: notification_deliveries.last_error
		// (stored in plaintext in DB), the delivery history API response (**bypassing channel config masking**),
		// server logs, and the 502 response returned to the frontend by the test-send endpoint.
		return nil, fmt.Errorf("request failed: %s", redactTransportError(err))
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, respBodyLimit))
	if readErr != nil {
		return nil, fmt.Errorf("failed to read response: %w", readErr)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, nil
	}
	// 429 (rate limit) and 408 (timeout) are worth retrying; other 4xx codes indicate configuration or permission issues where retrying is pointless.
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout {
		return nil, fmt.Errorf("remote rate-limited or timed out (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("remote server error (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	return nil, Permanent(fmt.Errorf("remote rejected request (HTTP %d): %s", resp.StatusCode, snippet(raw)))
}

// snippet compresses a response body into a short single-line string for error messages. Responses may contain newlines and excessive whitespace
// that would break the layout of the delivery history page if stored raw in last_error.
func snippet(raw []byte) string {
	return OneLine(string(raw), 200)
}

// redactRequestTarget reduces a delivery URL to "scheme://host/…" for use in error messages.
//
// This is the sole URL redaction rule in this package, intentionally kept **blunt**: everything except scheme and host
// is discarded. There is no generic and safe way to tell which part of a URL holds credentials:
//
//	DingTalk   credential in query        /robot/send?access_token=xxx
//	WeCom      credential in query        /cgi-bin/webhook/send?key=xxx
//	Feishu     credential in **last path segment** /open-apis/bot/v2/hook/<hook_id>
//	Telegram   credential in **middle path segment** /bot<token>/sendMessage
//
// Trying to "keep only the non-sensitive parts" would require per-channel rules, where missing any one channel means a credential leak.
// Keeping the host is sufficient for troubleshooting (DNS failures, connection refusals, and certificate mismatches can all be pinpointed),
// while the masked tail hint in the channel configuration identifies which specific bot it is.
//
// Returns a fixed placeholder on parse failure—never echoes the raw string back.
func redactRequestTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(unparseable URL)"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// redactTransportError strips the URL from a transport-layer error, keeping only the underlying cause.
//
// *url.Error has the structure {Op, URL, Err}, and its Error() prints the full URL.
// Here we explicitly extract the Err field to bypass its Error() method—more reliable than post-hoc string replacement,
// which would have to handle various URL-encoded/escaped forms and could easily miss edge cases.
func redactTransportError(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		host := ""
		if u, parseErr := url.Parse(uerr.URL); parseErr == nil {
			host = u.Host
		}
		if uerr.Err != nil {
			return fmt.Sprintf("%s %s: %s", uerr.Op, host, uerr.Err)
		}
		return fmt.Sprintf("%s %s: unknown error", uerr.Op, host)
	}
	// Non-*url.Error errors (such as those returned by redirect policies) may also contain URLs; run them through text redaction.
	return redactURLsInText(err.Error())
}

// redactURLsInText replaces any http(s) URLs appearing in a string with their redacted form.
//
// Used as a fallback for errors without structured fields (redirect policy errors, custom errors from third-party libraries).
// Recognizes http:// and https:// prefixes and splits on whitespace and quotes—URLs never contain those characters.
func redactURLsInText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		rest := s[i:]
		if strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://") {
			end := len(rest)
			if j := strings.IndexAny(rest, " \t\n\"'"); j >= 0 {
				end = j
			}
			b.WriteString(redactRequestTarget(rest[:end]))
			i += end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
