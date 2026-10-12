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

// emailDialTimeout / emailSessionTimeout bound connection setup and the whole SMTP session respectively.
// net/smtp has no timeout mechanism of its own; without these two, a stuck peer would hang the delivery goroutine
// forever, and since the dispatcher processes serially in a single goroutine, the whole notification system would stall.
const (
	emailDialTimeout    = 10 * time.Second
	emailSessionTimeout = 45 * time.Second
)

// emailChannel implements SMTP email delivery.
type emailChannel struct{}

func (emailChannel) Kind() string { return KindEmail }

// Email has no platform rate limit, but it shouldn't be used to flood; give a generous default.
func (emailChannel) DefaultRatePerMin() int { return 60 }

// Only the password is masked. SMTP host, account and recipients are not secrets; masking them would just make editing tedious.
func (emailChannel) SecretKeys() []string { return []string{"password"} }

// host/port decide which server receives the password; tls decides whether transport is encrypted. Any change to
// these three requires re-addressing the password, which also means "turning off TLS" must explicitly carry credentials rather than being a casual edit.
func (emailChannel) DestinationKeys() []string { return []string{"host", "port", "tls"} }

func (emailChannel) Validate(cfg map[string]any) error {
	if cfgString(cfg, "host") == "" {
		return errors.New("missing SMTP server address")
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

	// STARTTLS: upgrade if the peer supports it. Credentials can't be sent over a plaintext session (see the auth note below).
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
			// This is **correct** security behavior and must not be bypassed, but the reason needs to be spelled out;
			// otherwise users only see "unencrypted connection" and don't know what to do.
			if strings.Contains(err.Error(), "unencrypted connection") {
				return 0, Permanent(fmt.Errorf("refusing to send credentials: connection is not encrypted. Enable TLS, switch to port 465 (implicit TLS), or check \"Enable TLS\" (%w)", err))
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
		return 0, fmt.Errorf("failed to submit email: %w", err)
	}
	// A Quit failure doesn't change the fact that the server accepted the email, so its error is ignored.
	_ = client.Quit()
	// Email has no length truncation (the whole HTML body is sent), so the whole batch counts as delivered.
	return len(m.Items), nil
}

// emailDial establishes the SMTP connection.
//
// implicitTLS=true uses the "TLS on connect" style like port 465; false connects in plaintext on 25/587 and then
// does STARTTLS. They can't be mixed: sending a plaintext greeting to port 465 gets disconnected immediately.
//
// The session deadline is set **at connect time** (not afterwards), because net/smtp's Client hides the underlying
// connection in an unexported field that outsiders can't reach; once the connection is handed over, only a
// pre-set deadline can serve as a fallback. This also covers blocking during the handshake.
// Control hooks blockInternalDial, sharing the same guard as the HTTP channels. Without it SMTP would be a gap in
// the whole SSRF protection: a host of 169.254.169.254 or 127.0.0.1 could be connected directly, and when the
// smtp.NewClient handshake fails it wraps the peer's line into the error, which is echoed by the delivery history
// API via last_error, forming a semi-blind read primitive; the timing difference of "connection refused vs timeout"
// could also be used for port probing. The dial stage is the final enforcement point and also covers DNS rebinding.
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

// smtpStageError classifies a stage failure as "retryable" or "permanent" by SMTP reply code.
//
// Why the distinction matters: SMTP 4xx and 5xx mean completely different things:
//   - 4xx (450 greylisting, 451 local error, 452 insufficient storage) are **temporary** rejections,
//     and the proper approach is to retry later; greylisting in particular is hit on almost every first delivery.
//   - 5xx (550 no such user, 553 invalid address) are permanent rejections; retrying is pointless.
//
// If everything were treated as permanent, a mail server with greylisting would make **every** push land in failed
// after the first attempt, which is exactly the scenario where automatic retry should help most.
// The reply code is the first three digits of the error text; if no code can be parsed it is treated as retryable
// (better to try once more than to declare a possibly transient fault dead just because parsing failed).
func smtpStageError(what string, err error) error {
	code := smtpReplyCode(err.Error())
	if code >= 500 && code < 600 {
		return Permanent(fmt.Errorf("%s: %w", what, err))
	}
	return fmt.Errorf("%s: %w", what, err)
}

// smtpReplyCode extracts the leading three-digit reply code from SMTP error text, returning 0 if there is none.
// net/smtp doesn't export an error code field, so it must be read from the text; the format is "450 4.7.1 ...".
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

// buildEmailMessage assembles a complete RFC 5322 email.
//
// The body is base64-encoded for two reasons: SMTP limits lines to 1000 bytes, and HTML bodies (especially digests)
// easily produce longer lines; and base64 never produces lines starting with ".", avoiding SMTP dot-stuffing.
func buildEmailMessage(from string, to []string, m Message) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	// Non-ASCII subjects must be RFC 2047 encoded, otherwise clients display them garbled.
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", htmlTitle(m)))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	// Email has no hard length limit, so the body isn't truncated.
	b.WriteString("\r\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(htmlBody(m, 0)))
	// base64 is wrapped at 76 characters per RFC 2045.
	for len(encoded) > 76 {
		b.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	b.WriteString(encoded + "\r\n")
	return b.String(), nil
}
