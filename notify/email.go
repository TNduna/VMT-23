package notify

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// emailDialTimeout / emailSessionTimeout bound connection establishment and the overall SMTP session respectively.
// net/smtp has no built-in timeouts of its own; without these two bounds, a stalled upstream would hang
// the delivery goroutine forever—and since the dispatcher processes deliveries serially on a single goroutine,
// the entire notification system would grind to a halt.
const (
	emailDialTimeout    = 10 * time.Second
	emailSessionTimeout = 45 * time.Second
)

// emailChannel implements SMTP email delivery.
type emailChannel struct{}

func (emailChannel) Kind() string { return KindEmail }

// Email has no fixed platform rate limit, but shouldn't flood inboxes; set a generous default.
func (emailChannel) DefaultRatePerMin() int { return 60 }

// Mask only the password. SMTP host, username, and recipients are not secrets, and masking them would only make editing harder.
func (emailChannel) SecretKeys() []string { return []string{"password"} }

// host/port determine which server receives the password; tls determines whether transport is encrypted. Changing any of the three
// requires re-confirming the password—which also ensures that disabling TLS requires explicitly re-entering credentials rather than a casual toggle.
func (emailChannel) DestinationKeys() []string { return []string{"host", "port", "tls"} }

func (emailChannel) Validate(cfg map[string]any) error {
	if cfgString(cfg, "host") == "" {
		return errors.New("missing SMTP server host")
	}
	port := cfgInt(cfg, "port")
	if port <= 0 || port > 65535 {
		return errors.New("invalid SMTP port (must be 1-65535)")
	}
	if cfgString(cfg, "from") == "" {
		return errors.New("missing sender address")
	}
	if len(cfgStrings(cfg, "to")) == 0 {
		return errors.New("at least one recipient address is required")
	}
	return nil
}

func (c emailChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	host := cfgString(cfg, "host")
	port := cfgInt(cfg, "port")
	from := cfgString(cfg, "from")
	to := cfgStrings(cfg, "to")
	username := cfgString(cfg, "username")
	password := cfgString(cfg, "password")
	implicitTLS := cfgBool(cfg, "tls")

	msg, err := buildEmailMessage(from, to, m)
	if err != nil {
		return 0, Permanent(err)
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	client, err := emailDial(ctx, addr, host, implicitTLS)
	if err != nil {
		return 0, err
	}
	defer client.Close()

	// STARTTLS: upgrade if the server advertises support. Credentials must not be sent over plaintext sessions (see auth note below).
	if !implicitTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				return 0, fmt.Errorf("STARTTLS failed: %w", err)
			}
		}
	}
	if username != "" {
		if err := client.Auth(smtp.PlainAuth("", username, password, host)); err != nil {
			// smtp.PlainAuth refuses to send credentials over an unencrypted connection (unless the target is localhost).
			// This is the **correct** security behavior and must not be bypassed, but we explain the cause clearly—
			// otherwise the operator only sees "unencrypted connection" without knowing how to fix it.
			if strings.Contains(err.Error(), "unencrypted connection") {
				return 0, Permanent(fmt.Errorf("refusing to send credentials over an unencrypted connection: please enable TLS, or switch to port 465 (implicit TLS) (%w)", err))
			}
			return 0, Permanent(fmt.Errorf("SMTP authentication failed: %w", err))
		}
	}
	if err := client.Mail(from); err != nil {
		return 0, smtpStageError(fmt.Sprintf("sender %s rejected", from), err)
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return 0, smtpStageError(fmt.Sprintf("recipient %s rejected", rcpt), err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return 0, fmt.Errorf("SMTP DATA failed: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return 0, fmt.Errorf("failed to write email body: %w", err)
	}
	if err := w.Close(); err != nil {
		return 0, fmt.Errorf("failed to commit email: %w", err)
	}
	// A Quit error does not affect the fact that the server has already accepted the message, so ignore it.
	_ = client.Quit()
	// Email does not truncate body content (the entire HTML body is sent), so the full batch counts as delivered.
	return len(m.Items), nil
}

// emailDial establishes an SMTP connection.
//
// implicitTLS=true uses implicit TLS on connect (port 465); false connects in plaintext (port 25/587) and upgrades via
// STARTTLS. The two cannot be mixed: sending a plaintext greeting to port 465 causes an immediate disconnect.
//
// The session deadline is set **at dial time** (rather than after NewClient), because net/smtp's Client hides the underlying
// net.Conn in an unexported field; once handed over, the pre-configured deadline is the only safety net, which also covers hangs during the initial greeting handshake.
// Attaching blockInternalDial to Control shares the same SSRF guard as HTTP-based channels. Without it, SMTP would be a gap
// in SSRF protection: setting host to 169.254.169.254 or 127.0.0.1 would connect directly, and when smtp.NewClient fails the handshake,
// it includes the remote banner line in the error echoed via last_error in the delivery history API (a semi-blind read primitive),
// while "connection refused vs timeout" timing differences could be used for port scanning. Enforcing at dial time also covers DNS rebinding.
func emailDial(ctx context.Context, addr, host string, implicitTLS bool) (*smtp.Client, error) {
	d := &net.Dialer{Timeout: emailDialTimeout, Control: blockInternalDial}
	var conn net.Conn
	var err error
	if implicitTLS {
		conn, err = tls.DialWithDialer(d, "tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to connect to SMTP server: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(emailSessionTimeout))
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SMTP handshake failed: %w", err)
	}
	return client, nil
}

// smtpStageError classifies an SMTP stage error as "retryable" or "permanent" based on the SMTP reply code.
//
// Why this distinction is necessary: SMTP 4xx and 5xx have completely different semantics—
//   - 4xx (450 greylisting, 451 local error, 452 insufficient storage) is a **temporary** rejection
//     where retrying later is standard practice; greylisting in particular affects almost every first delivery attempt.
//   - 5xx (550 user unknown, 553 bad address syntax) is a permanent rejection where retrying is pointless.
//
// Treating all envelope errors as permanent would cause a mail server with greylisting enabled to mark **every single** notification
// as failed on the first attempt—when greylisting is the exact scenario automatic retries are meant to handle.
// Reply codes are parsed from the leading three digits of the error text; if no code can be parsed, the error is treated as retryable
// (better to retry once more than to permanently fail a transient network hiccup).
func smtpStageError(what string, err error) error {
	code := smtpReplyCode(err.Error())
	if code >= 500 && code < 600 {
		return Permanent(fmt.Errorf("%s: %w", what, err))
	}
	return fmt.Errorf("%s: %w", what, err)
}

// smtpReplyCode extracts the leading three-digit reply code from an SMTP error string, returning 0 if not found.
// net/smtp does not export the error code field on all error paths, so we parse it from the text formatted as "450 4.7.1 ...".
func smtpReplyCode(text string) int {
	if len(text) < 3 {
		return 0
	}
	n, err := strconv.Atoi(text[:3])
	if err != nil {
		return 0
	}
	return n
}

// buildEmailMessage constructs a complete RFC 5322 email message.
//
// The body is base64-encoded for two reasons: first, SMTP limits lines to 1000 bytes, and HTML bodies
// (especially digest emails) easily produce long lines; second, base64 naturally never produces lines starting with ".",
// avoiding SMTP dot-stuffing issues.
func buildEmailMessage(from string, to []string, m Message) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	// Non-ASCII subjects must be RFC 2047 encoded so email clients do not display mojibake.
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", htmlTitle(m)))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	// Email has no hard length cap, so the body is not truncated.
	b.WriteString("\r\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(htmlBody(m, 0)))
	// Wrap base64 at 76 characters per line in accordance with RFC 2045.
	for len(encoded) > 76 {
		b.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	b.WriteString(encoded + "\r\n")
	return b.String(), nil
}
