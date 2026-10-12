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

// allowLocalTargets decides whether messages may be delivered to loopback / link-local addresses.
//
// Denied by default. These ranges are not where IM bots or public mail servers live, and what they can reach is
// sensitive: admin ports of other services on the same host, and the cloud metadata endpoint
// (169.254.169.254, which exposes instance credentials). Delivery addresses are configured by admins, but an admin
// session hijacked via XSS/CSRF, or a second person sharing the same JWT, could read responses back by editing the
// config: doJSON writes the first 200 bytes of a 4xx/5xx response body into last_error, and the delivery history
// endpoint echoes it back, which is a semi-blind read primitive.
//
// But a "local SMTP relay" (postfix on 127.0.0.1:25) is a common self-hosted mail setup, and a blanket ban would
// block people. So an explicit escape hatch is provided instead of a hardcoded allowance:
// set ARTEX_NOTIFY_ALLOW_LOCAL=1 to allow it.
//
// It is exported as AllowLocalTargetsEnv so tests can explicitly enable it: test cases in this package and the
// server package use many httptest fake receivers on 127.0.0.1, which would all be blocked by the guard otherwise.
const AllowLocalTargetsEnv = "ARTEX_NOTIFY_ALLOW_LOCAL"

func allowLocalTargets() bool {
	v := strings.TrimSpace(os.Getenv(AllowLocalTargetsEnv))
	return v == "1" || strings.EqualFold(v, "true")
}

// isBlockedDialIP reports whether the target IP is in a range "not allowed for delivery by default".
//
// Only loopback, link-local (including cloud metadata 169.254.169.254), unspecified and multicast are rejected.
// RFC1918 private networks are **not** rejected: self-hosted Mattermost / SMTP relays on internal networks are a very
// common legitimate use, and blocking them would make the feature unusable in real environments. This trade-off is
// deliberate: the protection must stop truly sensitive targets without breaking normal deployments.
func isBlockedDialIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// IPv4-mapped IPv6 (::ffff:127.0.0.1) must be converted back to IPv4 before checking, otherwise it bypasses the check.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// blockInternalDial is the Control hook of the http.Transport dialer, checking the target address **when the
// connection is established**.
//
// Why at dial time rather than only when saving config: this is the point where it finally takes effect.
// It covers both ways of bypassing config validation: DNS rebinding (resolving to a public IP at validation time and
// to an internal one at connect time) and redirects (although cross-host redirects are refused, same-host redirects
// can still point the path elsewhere).
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
		return fmt.Errorf("cannot parse target address %q", host)
	}
	if isBlockedDialIP(ip) {
		return fmt.Errorf("refusing to deliver to local/link-local address %s (if you really need to deliver to a local service, set %s=1)", ip, AllowLocalTargetsEnv)
	}
	return nil
}

// notifyTransport adds only a dial guard on top of the default Transport.
// Clone keeps all default tuning (connection pool, HTTP/2, timeouts, proxy, etc.),
// so adding one check doesn't change any other behavior.
var notifyTransport = func() *http.Transport {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{}
	}
	clone := t.Clone()
	clone.DialContext = (&net.Dialer{Timeout: 10 * time.Second, Control: blockInternalDial}).DialContext
	return clone
}()

// httpClient is the client shared by all channel deliveries.
//
// It deliberately does **not** reuse the project's global egress proxy (GlobalProxy on the server side): that proxy
// is for pentest target traffic and is often an unstable tunnel, and notification availability should not be held
// hostage by target network jitter. IM pushes go direct. The timeout is 15 seconds; a peer slower than that is
// effectively already failing.
//
// Cross-host redirects are refused: delivery addresses here are all "one fixed endpoint" and normally never redirect
// to another host, while credentials for these platforms (DingTalk access_token, WeCom key, Telegram bot token)
// **are in the URL**, so following a cross-host redirect would hand the credentials to the redirect target.
// Same-host redirects (e.g. adding a trailing slash) are still allowed.
var httpClient = &http.Client{
	Timeout:   15 * time.Second,
	Transport: notifyTransport,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("refusing cross-host redirect (%s → %s)", via[0].URL.Host, req.URL.Host)
		}
		return nil
	},
}

// respBodyLimit caps how much of the response body is read. A misbehaving peer may return huge content, while we only
// need the error code and a short error description to show in delivery history.
const respBodyLimit = 8 << 10

// doJSON sends one request and returns the (length-limited) response body.
//
// When payload is nil an empty body is sent (for GET or platforms that don't require a body).
// Header key/values are attached as-is, used for generic webhook custom headers.
//
// Error classification is this function's core job: network failures and 5xx/408/429 are "retryable",
// other 4xx are "permanent failures"; retrying a 403 just logs the same error 3 times.
func doJSON(ctx context.Context, method, url string, headers map[string]string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			// A serialization failure is a local bug (wrong config field type); retrying won't help.
			return nil, Permanent(fmt.Errorf("failed to build request body: %w", err))
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		// Invalid URL: most likely the user entered the wrong address, a permanent failure.
		// err must not be passed through here either: url.Parse's error text contains the full address.
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
		// Connection refused, DNS failure, timeout: mostly transient faults, left to backoff retry.
		//
		// The error text must be redacted before being passed on. Reason: http.Client.Do returns a *url.Error whose
		// Error() is `Op "full URL": underlying error`, and credentials for these platforms **are in the URL**
		// (DingTalk access_token, WeCom key, Feishu hook id, Telegram /bot<token>/).
		// Without redaction, credentials would flow along this error string to four places: last_error in
		// notification_deliveries (stored in plaintext), the delivery history API response (**bypassing channel config
		// masking**), server logs, and the 502 text the test-send endpoint returns to the frontend.
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
	// 429 (rate limited) and 408 (timeout) are worth retrying; other 4xx are config or permission problems where retrying is pointless.
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout {
		return nil, fmt.Errorf("peer rate limited or timed out (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("peer service error (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	return nil, Permanent(fmt.Errorf("peer rejected request (HTTP %d): %s", resp.StatusCode, snippet(raw)))
}

// snippet compresses the response body into a short single line for error messages. Responses may contain newlines
// and lots of whitespace, which would break the delivery history page layout if put into last_error directly.
func snippet(raw []byte) string {
	return OneLine(string(raw), 200)
}

// redactRequestTarget reduces a delivery address to "scheme://host/…" for error messages.
//
// This is the package's single address-redaction rule, and it is deliberately **blunt**: everything except scheme
// and host is dropped. The reason is that there is no "general and safe" way to tell which part of a URL is a credential:
//
//	DingTalk  credential in query             /robot/send?access_token=xxx
//	WeCom     credential in query             /cgi-bin/webhook/send?key=xxx
//	Feishu    credential in **last path segment** /open-apis/bot/v2/hook/<hook_id>
//	Telegram  credential in **middle of path**    /bot<token>/sendMessage
//
// "Keeping only the useful parts" would require per-channel patches, and missing any one platform is a credential leak.
// Keeping the host is enough for troubleshooting (DNS failure, connection refused, bad certificate can all be pinpointed);
// which bot it is can be recognized from the masked suffix hint in the channel config.
//
// On parse failure a fixed placeholder is returned; the raw string is never echoed back.
func redactRequestTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(unparseable address)"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// redactTransportError strips the address from a transport-layer error, keeping only the underlying cause.
//
// *url.Error is {Op, URL, Err}, and its Error() prints the URL too.
// Here the Err field is taken explicitly, bypassing its Error(); this is more reliable than string replacement after
// the fact, which would have to correctly handle all the URL-encoded/escaped variants and is easy to get wrong.
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
	// Non-*url.Error errors (e.g. from the redirect policy) may also contain addresses, so redact them too.
	return redactURLsInText(err.Error())
}

// redactURLsInText replaces http(s) addresses appearing in text with their redacted form.
//
// A fallback for errors without structured fields (redirect policy errors, custom errors from third-party libraries).
// Only http/https prefixes are recognized, split on whitespace and quotes; addresses never contain those characters.
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
