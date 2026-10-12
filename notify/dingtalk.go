package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"
)

// dingTalkChannel implements the DingTalk custom bot.
//
// Platform characteristics (which drive the implementation trade-offs here):
//   - Each bot is rate limited to 20 messages/minute; excess messages are silently dropped (HTTP may still be 200),
//     so rate limiting must be done client side; see DefaultRatePerMin.
//   - One of three security settings: signing / custom keywords / IP allowlist. Signing is the only option that
//     doesn't depend on message content, so only signing is supported (plus a bare webhook with none enabled).
//   - Both success and failure return HTTP 200, distinguished by errcode in the body; not checking errcode
//     would record delivery failures as successes.
type dingTalkChannel struct{}

func (dingTalkChannel) Kind() string { return KindDingTalk }

func (dingTalkChannel) DefaultRatePerMin() int { return 20 }

// The DingTalk webhook URL contains the access_token and is itself a credential, so it is masked as a whole.
func (dingTalkChannel) SecretKeys() []string { return []string{"webhook", "secret"} }

// The destination is the DingTalk webhook URL itself; changing it requires re-addressing the signing secret for the new address.
func (dingTalkChannel) DestinationKeys() []string { return []string{"webhook"} }

func (dingTalkChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("missing Webhook URL")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("invalid Webhook URL: %w", err)
	}
	return nil
}

// Send delivers one message. Uses an ActionCard (with a button) for a single item with a detail link, otherwise markdown.
func (c dingTalkChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	hook := cfgString(cfg, "webhook")
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	endpoint, err := dingTalkSignedURL(hook, cfgString(cfg, "secret"), time.Now())
	if err != nil {
		return 0, Permanent(err)
	}

	title := markdownTitle(m)
	// DingTalk markdown has no explicit byte limit, but a cap is still applied to guard against abnormally bloated evidence fields.
	text, kept := markdownBody(m, 20000)

	var payload any
	if !m.Batch && len(m.Items) == 1 && m.Items[0].DetailURL != "" {
		payload = map[string]any{
			"msgtype": "actionCard",
			"actionCard": map[string]any{
				"title":          title,
				"text":           text,
				"btnOrientation": "0",
				"singleTitle":    "View details",
				"singleURL":      m.Items[0].DetailURL,
			},
		}
	} else {
		payload = map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]any{"title": title, "text": text},
		}
	}

	raw, err := doJSON(ctx, "POST", endpoint, nil, payload)
	if err != nil {
		return 0, err
	}
	// DingTalk hides business errors inside 200 responses.
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("failed to parse DingTalk response: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 301000 is a signature verification failure, 310000 is a keyword mismatch: both are config errors
		// that retrying won't fix.
		return 0, Permanent(fmt.Errorf("DingTalk returned error %d: %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}

// dingTalkSignedURL appends timestamp and sign parameters to the webhook per the official signing rules.
//
// Rule: string to sign = timestamp + "\n" + secret, and the HMAC-SHA256 **key is also the secret**;
// the result is base64-encoded then URL-encoded. timestamp is in milliseconds. With an empty secret the hook is
// returned unchanged, to support bots without signing enabled.
func dingTalkSignedURL(hook, secret string, now time.Time) (string, error) {
	if secret == "" {
		return hook, nil
	}
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "\n" + secret))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	u, err := url.Parse(hook)
	if err != nil {
		// Don't pass err through: url.Parse's error text contains the full address (including access_token).
		return "", fmt.Errorf("failed to parse Webhook URL: %s", redactRequestTarget(hook))
	}
	q := u.Query()
	q.Set("timestamp", ts)
	q.Set("sign", sign)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// validateHTTPURL checks that the address is usable and the scheme supported, and applies the internal-address check to literal IP targets.
//
// Two subtleties:
//
//  1. **Error messages must be redacted**. url.Parse itself returns a *url.Error whose Error() includes the
//     **full original address**, and the addresses for these platforms embed credentials (DingTalk access_token,
//     WeCom key, Telegram bot token, Feishu hook id). This used to `return err` directly, so the "malformed address"
//     error carried the credentials out, into the test endpoint's 400 response, the last_error stored on every
//     delivery, server logs and the delivery history API.
//
//  2. **Literal IPs are checked for internal ranges directly**, while hostnames are left to dial time (blockInternalDial
//     is the final enforcement point and also covers DNS rebinding). Checking here gives feedback when the config is
//     saved instead of waiting for the first delivery failure.
//
// Restricting schemes is defensive: file:// / gopher:// etc. could cause unexpected http.Client behavior
// (already blocked by the scheme check, but there's no reason to open that surface).
func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("cannot parse address (%s)", redactRequestTarget(raw))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("only http/https are supported, got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("missing hostname")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && isBlockedDialIP(ip) && !allowLocalTargets() {
		return fmt.Errorf("refusing to deliver to local/link-local address %s (if you really need to deliver to a local service, set %s=1)", ip, AllowLocalTargetsEnv)
	}
	return nil
}
