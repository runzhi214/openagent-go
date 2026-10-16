package server

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// NotifySIGHUP installs a SIGHUP handler and returns its channel. SIGHUP is
// the settings hot-reload trigger (nginx/sshd convention); without a handler
// the default action terminates the process, so a terminal hangup or an
// operator mistake would kill a running server. Installed once in main
// before any subcommand runs, covering the whole process lifetime.
func NotifySIGHUP() <-chan os.Signal {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	return ch
}

// RunReloadSignalLoop reloads settings once per received SIGHUP, exiting
// when ctx is cancelled. Signals queued while a reload is in flight are
// coalesced into the next one, so a burst collapses to at most one queued
// reload instead of N.
func RunReloadSignalLoop(ctx context.Context, sigCh <-chan os.Signal) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-sigCh:
			drainSignals(sigCh)
			reloadOnce()
		}
	}
}

// drainSignals empties sigCh without blocking, collapsing signals queued
// during a reload into the single reload that follows.
func drainSignals(sigCh <-chan os.Signal) {
	for {
		select {
		case <-sigCh:
		default:
			return
		}
	}
}

// reloadOnce runs one settings reload through the shared reload entry point
// — the same one behind the /settings reload slash command and the settings
// tool's reload action. No server running (no reload function stored) is a
// no-op: short-lived subcommands never register one.
func reloadOnce() {
	fn := loadSettingsReloadFn()
	if fn == nil {
		slog.Debug("SIGHUP: no server running, nothing to reload")
		return
	}
	res := fn(context.Background())
	switch {
	case res.ParseError != "":
		slog.Warn("SIGHUP reload failed, keeping previous config", "error", res.ParseError)
	case len(res.Violations) > 0:
		slog.Warn("SIGHUP reload blocked by validation violations, keeping previous config",
			"violations", res.Violations)
	default:
		slog.Info("SIGHUP reload ok", "applied", res.Applied)
	}
}
