package notify

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// This file covers two interrelated security fortifications:
//   1. Outbound delivery targets must not use the server as a proxy to hit internal networks or cloud metadata (SSRF).
//   2. Address validation error messages must not leak credentials embedded within URLs.
//
// Test environment note: Many test cases in this package use fake httptest receivers on 127.0.0.1,
// which the dial guard blocks by default. Thus TestMain enables AllowLocalTargetsEnv, while each SSRF test
// explicitly clears it to assert default-deny behavior.

func TestMain(m *testing.M) {
	// Allow standard test cases to connect to local fake receivers; SSRF tests clear this explicitly.
	_ = os.Setenv(AllowLocalTargetsEnv, "1")
	os.Exit(m.Run())
}

// TestDialGuardRejectsLoopbackByDefault is the core assertion for SSRF defense:
// Under default configuration, dispatch to loopback addresses must be rejected at the transport/dial stage.
func TestDialGuardRejectsLoopbackByDefault(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = io.WriteString(w, `{"errcode":0}`)
	}))
	defer srv.Close()

	t.Setenv(AllowLocalTargetsEnv, "") // Disable escape hatch = default behavior
	_, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"}, Message{Items: []Item{{Severity: "high"}}})
	if err == nil {
		t.Fatal("dispatch to loopback address should not be permitted by default")
	}
	if hit {
		t.Fatal("request reached local server — dial guard failed to intercept")
	}
	// Error message should guide user on how to explicitly opt in (local SMTP relay is a valid setup).
	if !strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("refusal message should explain how to explicitly permit: %v", err)
	}
}

// TestDialGuardAllowsLoopbackWhenOptedIn: when explicitly enabled, local dispatch must succeed,
// otherwise legitimate deployments like local postfix or internal relays will break.
func TestDialGuardAllowsLoopbackWhenOptedIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer srv.Close()

	t.Setenv(AllowLocalTargetsEnv, "1")
	if _, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"}, Message{Items: []Item{{Severity: "high"}}}); err != nil {
		t.Fatalf("should permit delivery after explicit opt-in: %v", err)
	}
}

func TestIsBlockedDialIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.1.2.3", "::1",
		"169.254.169.254", // Cloud metadata endpoint — primary reason for this check
		"169.254.1.1", "fe80::1",
		"0.0.0.0", "::",
		"224.0.0.1", "ff02::1",
		"::ffff:127.0.0.1", // IPv4-mapped forms must be normalized before evaluation, otherwise bypass
		"",
	}
	for _, s := range blocked {
		if !isBlockedDialIP(net.ParseIP(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	// RFC1918 private subnets are deliberately permitted: internal Mattermost / SMTP relays are common.
	// This assertion protects against accidental regression.
	allowed := []string{"10.0.0.5", "172.16.3.4", "192.168.1.10", "8.8.8.8", "2606:4700::1111"}
	for _, s := range allowed {
		if isBlockedDialIP(net.ParseIP(s)) {
			t.Errorf("%s should be allowed (private networks are legitimate delivery destinations)", s)
		}
	}
}

// TestValidateHTTPURLRejectsLiteralPrivateTargets covers configuration-time checks:
// Literal private IPs should be rejected at save time rather than waiting for first dispatch failure.
func TestValidateHTTPURLRejectsLiteralPrivateTargets(t *testing.T) {
	t.Setenv(AllowLocalTargetsEnv, "")
	for _, raw := range []string{
		"http://127.0.0.1:8080/hook",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]:8080/hook",
	} {
		if err := validateHTTPURL(raw); err == nil {
			t.Errorf("%s should be rejected at config stage", raw)
		}
	}
	// Public addresses and RFC1918 private addresses pass configuration check (private checked at dial time).
	for _, raw := range []string{"https://oapi.dingtalk.com/robot/send", "http://10.0.0.9/hook"} {
		if err := validateHTTPURL(raw); err != nil {
			t.Errorf("%s should pass validation: %v", raw, err)
		}
	}
}

// TestValidateHTTPURLErrorNeverLeaksCredentials covers an audit finding:
// When url.Parse fails it returns *url.Error containing the full raw URL string.
func TestValidateHTTPURLErrorNeverLeaksCredentials(t *testing.T) {
	cases := []string{
		"http://127.0.0.1/%zz?access_token=" + leakProbeToken,         // Invalid percent escape
		"https://a.example.com:port/x?access_token=" + leakProbeToken, // Non-numeric port
		"http://[::1?access_token=" + leakProbeToken,                  // Unpaired bracket
	}
	for _, raw := range cases {
		// Confirm url.Parse actually fails on this input.
		if _, err := url.Parse(raw); err == nil {
			t.Errorf("%q should fail url.Parse, otherwise target branch is not exercised", raw)
			continue
		}
		err := validateHTTPURL(raw)
		if err == nil {
			t.Errorf("%q should fail validation", raw)
			continue
		}
		assertNoSecret(t, err.Error(), leakProbeToken)
	}
	// Verify channel wrapper also does not leak the address.
	t.Setenv(AllowLocalTargetsEnv, "")
	err := (dingTalkChannel{}).Validate(map[string]any{"webhook": cases[0]})
	if err == nil {
		t.Fatal("invalid address should fail validation")
	}
	assertNoSecret(t, err.Error(), leakProbeToken)
}

// TestEmailDialGuardRejectsLoopbackByDefault covers the dial guard on SMTP channels.
func TestEmailDialGuardRejectsLoopbackByDefault(t *testing.T) {
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)

	t.Setenv(AllowLocalTargetsEnv, "") // Disable escape hatch = default behavior
	_, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil {
		t.Fatal("dispatching email to loopback address should not be permitted by default")
	}
	// Connection should not be established: blocked in Control hook before EHLO is sent.
	if f.sawCommand("EHLO") || f.sawCommand("HELO") {
		t.Fatal("SMTP session was established — dial guard failed to intercept")
	}
	// Error message should guide user on how to explicitly opt in.
	if !strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("refusal message should explain how to explicitly permit: %v", err)
	}
}

// TestEmailDialGuardAllowsLoopbackWhenOptedIn: when explicitly opted in,
// local relay delivery must succeed.
func TestEmailDialGuardAllowsLoopbackWhenOptedIn(t *testing.T) {
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)

	t.Setenv(AllowLocalTargetsEnv, "1")
	if _, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("local SMTP delivery should succeed after explicit opt-in: %v", err)
	}
	if !f.sawCommand("EHLO") {
		t.Fatal("EHLO command was not observed — session was not properly established")
	}
}
