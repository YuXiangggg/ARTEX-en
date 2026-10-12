package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// weComMarkdownLimit is the hard limit of WeCom group bot markdown content (bytes, not characters).
// It is the tightest limit among all six channels, and the main reason TruncateBytes exists.
const weComMarkdownLimit = 4096

// weComChannel implements the WeCom group bot.
//
// Platform characteristics:
//   - Authenticated solely by the key in the URL, no signing support, so the webhook URL itself is the entire credential.
//   - markdown content is limited to 4096 **bytes**, and oversized messages are rejected entirely (not truncated).
//     At 3 bytes per CJK character that leaves only about a thousand characters, so truncation must happen client side.
//   - Rate limited to 20 messages/minute, also covered by client-side rate limiting.
type weComChannel struct{}

func (weComChannel) Kind() string { return KindWeCom }

func (weComChannel) DefaultRatePerMin() int { return 20 }

// WeCom has only one credential, the webhook (the key in the URL), and no signing support:
// the whole URL is the entire credential, and no other field needs masking.
func (weComChannel) SecretKeys() []string { return []string{"webhook"} }

// WeCom has only the webhook field, which is both destination and credential, so there is no "leftover credential after changing the address".
func (weComChannel) DestinationKeys() []string { return []string{"webhook"} }

func (weComChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("missing Webhook URL")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("invalid Webhook URL: %w", err)
	}
	return nil
}

func (c weComChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	// A digest batch can be long (50 items × one line each + prefix), easily exceeding 4096 bytes.
	// Truncate here rather than relying on platform errors: rejection loses the whole batch, while truncation delivers at least the first several items.
	content, kept := markdownBody(m, weComMarkdownLimit)
	payload := map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"content": content},
	}
	raw, err := doJSON(ctx, "POST", cfgString(cfg, "webhook"), nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("failed to parse WeCom response: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 45009 means the API call limit was exceeded; the platform's rate window rolls, so retrying after backoff works,
		// and it is explicitly classified as retryable. Reaching here means the client rate_per_min is too aggressive;
		// retrying is only a fallback, and the real fix is lowering the channel's rate limit.
		if res.ErrCode == 45009 {
			return 0, fmt.Errorf("WeCom rate limited %d: %s", res.ErrCode, res.ErrMsg)
		}
		// 93000 means the webhook key is invalid: a permanent failure that retrying won't fix.
		return 0, Permanent(fmt.Errorf("WeCom returned error %d: %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}
