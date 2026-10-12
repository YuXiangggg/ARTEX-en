package agent

import (
	"log"
	"path/filepath"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/noaadapter"
)

// noaWarn returns a diagnostics sink tagging non-fatal noa messages with the
// session, routed through the package logger (agents have no per-instance one).
func noaWarn(session string) func(string) {
	return func(msg string) { log.Printf("[noa] %s: %s", session, msg) }
}

// noa is the "model-driven context compaction" mechanism introduced in norma v0.4.0,
// toggled by the user in system settings as a platform experimental feature. It is
// mutually exclusive with built-in compaction: noaadapter.Enable is the only entry point;
// it installs the context takeover (Compactor), the Compress tool, and three resident
// prompt segments in one go. Not calling Enable means it is off (built-in compaction
// works as usual). The toggle is resolved by the noaEnabledFn injected per agent, read
// once per run, so switching it only affects runs started afterwards, with no need to
// rebuild the agent.

// enableNoa wires noa into opts when the resolver reports it is on. archiveRoot is the
// persistent base directory for the original text of compactions (taken from the global
// workDir; all agents land under <workDir>/noa, not scattered across task/intent dirs),
// and sessionID names the archive subdirectory under it (globally unique, so no conflict
// within the same base directory).
//
// noa is experimental: a wiring failure must not interrupt a real task. On error it is
// reported via onWarn and falls back to built-in compaction. On successful enablement it
// clears opts.Compaction to avoid agentcore warning about "two context managers set at once".
func enableNoa(opts *agentcore.Options, enabled func() bool, archiveRoot, sessionID string, onWarn func(string)) {
	if enabled == nil || !enabled() {
		return
	}
	if opts.OnWarn == nil {
		opts.OnWarn = onWarn
	}
	if err := noaadapter.Enable(opts, noaadapter.Options{
		ArchiveBaseDir: filepath.Join(archiveRoot, "noa"),
		SessionID:      sessionID,
		OnWarn:         onWarn,
	}); err != nil {
		if onWarn != nil {
			onWarn("noa compaction failed to enable, falling back to built-in compaction: " + err.Error())
		}
		return
	}
	// Compactor overrides Compaction, but when both are set agentcore warns every time; clear it explicitly.
	opts.Compaction = nil
}
