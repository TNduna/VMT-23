package agent

import (
	"log"
	"path/filepath"

	"github.com/TNduna/norma/agentcore"
	"github.com/TNduna/norma/noaadapter"
)

// noaWarn returns a diagnostics sink tagging non-fatal noa messages with the
// session, routed through the package logger (agents have no per-instance one).
func noaWarn(session string) func(string) {
	return func(msg string) { log.Printf("[noa] %s: %s", session, msg) }
}

// noa is the model-driven context compression mechanism introduced in norma v0.4.0,
// exposed as an experimental platform feature toggled in system settings. It is
// mutually exclusive with built-in compaction: noaadapter.Enable is the single entry
// point that attaches the context Compactor, the Compress tool, and the three resident
// prompt segments at once; not calling Enable leaves it off (built-in compaction works
// as usual). The toggle is resolved per run via noaEnabledFn injected into each agent,
// so switching it affects subsequent runs without rebuilding the agent.

// enableNoa wires noa into opts when the resolver reports it enabled. archiveRoot is
// the persistent base directory for compressed originals (using the global workDir so
// all agents land under <workDir>/noa rather than scattering across task/intent dirs),
// and sessionID names the archive subdirectory under it (globally unique within base).
//
// noa is experimental: failure to attach must not abort a real task. On error it reports
// via onWarn and falls back to built-in compaction. When enabled successfully, it clears
// opts.Compaction so agentcore does not warn about both context managers being set.
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
			onWarn("failed to enable noa compression, falling back to built-in compaction: " + err.Error())
		}
		return
	}
	// Compactor overrides Compaction, but agentcore warns every time if both are set; clear it explicitly.
	opts.Compaction = nil
}
