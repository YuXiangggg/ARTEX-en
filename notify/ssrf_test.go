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

// This file covers two related hardening measures:
//   (1) delivery addresses must not use the server as a pivot into internal networks / cloud metadata (SSRF)
//   (2) address validation error messages must not carry credentials from the address
//
// About the test environment: many cases in this package use httptest fake receivers on 127.0.0.1, which the guard
// blocks by default. So TestMain enables AllowLocalTargetsEnv globally, and each SSRF case below explicitly clears
// it to assert the **deny-by-default** behavior.

func TestMain(m *testing.M) {
	// Let regular cases reach local fake receivers; SSRF cases temporarily clear it themselves.
	_ = os.Setenv(AllowLocalTargetsEnv, "1")
	os.Exit(m.Run())
}

// TestDialGuardRejectsLoopbackByDefault is the core assertion of the SSRF protection:
// under default config, delivery to a loopback address must be rejected **at the connection layer**.
func TestDialGuardRejectsLoopbackByDefault(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = io.WriteString(w, `{"errcode":0}`)
	}))
	defer srv.Close()

	t.Setenv(AllowLocalTargetsEnv, "") // escape hatch off = default behavior
	_, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"}, Message{Items: []Item{{Severity: "high"}}})
	if err == nil {
		t.Fatal("delivery to loopback addresses should not be allowed by default")
	}
	if hit {
		t.Fatal("the request reached the local service; the guard did not work")
	}
	// The error message must tell users how to allow it (a local SMTP relay is a legitimate setup).
	if !strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("the rejection message should explain how to explicitly allow it: %v", err)
	}
}

// TestDialGuardAllowsLoopbackWhenOptedIn is the reverse case: it must work once explicitly enabled,
// otherwise legitimate deployments like local postfix / internal relays would be broken by a blanket ban.
func TestDialGuardAllowsLoopbackWhenOptedIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer srv.Close()

	t.Setenv(AllowLocalTargetsEnv, "1")
	if _, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"}, Message{Items: []Item{{Severity: "high"}}}); err != nil {
		t.Fatalf("delivery should work once explicitly allowed: %v", err)
	}
}

func TestIsBlockedDialIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.1.2.3", "::1",
		"169.254.169.254", // cloud metadata endpoint: the main reason this function exists
		"169.254.1.1", "fe80::1",
		"0.0.0.0", "::",
		"224.0.0.1", "ff02::1",
		"::ffff:127.0.0.1", // IPv4-mapped form must be converted before checking, otherwise it is a bypass
		"",
	}
	for _, s := range blocked {
		if !isBlockedDialIP(net.ParseIP(s)) {
			t.Errorf("%s should be rejected", s)
		}
	}
	// RFC1918 private networks are **deliberately allowed**: self-hosted Mattermost / SMTP relays on internal networks are common legitimate uses.
	// This assertion pins down that trade-off: if someone casually adds a private-network check later, this fails,
	// forcing a conscious decision (rather than silently breaking a batch of deployments).
	allowed := []string{"10.0.0.5", "172.16.3.4", "192.168.1.10", "8.8.8.8", "2606:4700::1111"}
	for _, s := range allowed {
		if isBlockedDialIP(net.ParseIP(s)) {
			t.Errorf("%s should be allowed (private networks are common legitimate delivery targets)", s)
		}
	}
}

// TestValidateHTTPURLRejectsLiteralPrivateTargets covers the early feedback at config time:
// literal IPs should be rejected on save rather than waiting for the first delivery failure.
func TestValidateHTTPURLRejectsLiteralPrivateTargets(t *testing.T) {
	t.Setenv(AllowLocalTargetsEnv, "")
	for _, raw := range []string{
		"http://127.0.0.1:8080/hook",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]:8080/hook",
	} {
		if err := validateHTTPURL(raw); err == nil {
			t.Errorf("%s should be rejected at config time", raw)
		}
	}
	// Public and private addresses pass as usual (private ones are left to the dial stage, which doesn't block them).
	for _, raw := range []string{"https://oapi.dingtalk.com/robot/send", "http://10.0.0.9/hook"} {
		if err := validateHTTPURL(raw); err != nil {
			t.Errorf("%s should pass validation: %v", raw, err)
		}
	}
}

// TestValidateHTTPURLErrorNeverLeaksCredentials covers the branch an audit found missing in the previous round.
//
// When url.Parse **fails** it returns a *url.Error whose Error() contains the full original address. The previous
// round only redacted errors returned by http.Client.Do and missed this one; and the "permanent failure path" cases
// added then (file://, gopher://, ftp://) all actually parse successfully with url.Parse and take the scheme branch,
// so all-green proved nothing about this path's safety: a false guarantee.
func TestValidateHTTPURLErrorNeverLeaksCredentials(t *testing.T) {
	cases := []string{
		"http://127.0.0.1/%zz?access_token=" + leakProbeToken,         // invalid percent escape
		"https://a.example.com:port/x?access_token=" + leakProbeToken, // non-numeric port
		"http://[::1?access_token=" + leakProbeToken,                  // unbalanced bracket
	}
	for _, raw := range cases {
		// First confirm this input **really** makes url.Parse fail. Without this step the case could silently
		// take another branch (that is exactly how the previous false guarantee arose).
		if _, err := url.Parse(raw); err == nil {
			t.Errorf("%q should have failed to parse, otherwise this case does not cover the target branch", raw)
			continue
		}
		err := validateHTTPURL(raw)
		if err == nil {
			t.Errorf("%q should fail validation", raw)
			continue
		}
		assertNoSecret(t, err.Error(), leakProbeToken)
	}
	// Confirm the channel-level wrapping doesn't carry the address out either.
	t.Setenv(AllowLocalTargetsEnv, "")
	err := (dingTalkChannel{}).Validate(map[string]any{"webhook": cases[0]})
	if err == nil {
		t.Fatal("an invalid address should fail validation")
	}
	assertNoSecret(t, err.Error(), leakProbeToken)
}

// TestEmailDialGuardRejectsLoopbackByDefault covers the SMTP channel's dial guard.
//
// The email channel used to use a bare net.Dialer, the only gap in the whole SSRF protection: a host of
// 169.254.169.254 or 127.0.0.1 could be connected directly, and when the smtp.NewClient handshake failed it wrapped
// the peer's line into the error, echoed by the delivery history API via last_error; exactly the semi-blind read
// primitive the other channels had already closed. The timing difference of "connection refused vs timeout" could
// also be used for port probing.
//
// This package's TestMain enables AllowLocalTargetsEnv globally (many cases use fake receivers on 127.0.0.1),
// so this case must clear it itself; otherwise it would pass with or without the guard, which is exactly why the
// gap wasn't caught by any test originally.
func TestEmailDialGuardRejectsLoopbackByDefault(t *testing.T) {
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)

	t.Setenv(AllowLocalTargetsEnv, "") // escape hatch off = default behavior
	_, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil {
		t.Fatal("email delivery to loopback addresses should not be allowed by default")
	}
	// The connection should never be established: the guard blocks it in the Control hook, so EHLO is never sent.
	if f.sawCommand("EHLO") || f.sawCommand("HELO") {
		t.Fatal("SMTP session was established; the guard did not work")
	}
	// The error message must tell users how to allow it (a local postfix relay is a legitimate setup).
	if !strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("the rejection message should explain how to explicitly allow it: %v", err)
	}
}

// TestEmailDialGuardAllowsLoopbackWhenOptedIn is the paired reverse case: once explicitly enabled, delivery must
// work normally. Self-hosted internal SMTP / local relays are very common deployments, and the guard must not be a blanket ban.
func TestEmailDialGuardAllowsLoopbackWhenOptedIn(t *testing.T) {
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)

	t.Setenv(AllowLocalTargetsEnv, "1")
	if _, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("local SMTP delivery should work once explicitly allowed: %v", err)
	}
	if !f.sawCommand("EHLO") {
		t.Fatal("no EHLO seen; the session was not really established")
	}
}
