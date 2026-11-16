// Package mcp exposes an up server to agent clients over the Model Context
// Protocol (MCP).
//
// Every tool works through Backend, the same seam the TUI uses: the remote
// client and a local store both implement it, so a tool never forks into a
// per-backend path. The transport is stdio and the client owns the process,
// so nothing here prompts: credentials come from the profile 'up login' wrote,
// and a missing one is an error the client shows from stderr.
package mcp

import (
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/heohlee/up/internal/tui"
)

// Backend is what the tools need from a store: the interface the TUI uses for
// per-resource reads and writes, plus the whole-snapshot loader, so a remote
// server costs one request per tool call. *client.Client implements both.
type Backend interface {
	tui.Store
	tui.SnapshotLoader
}

// Options configures the server.
type Options struct {
	// Destination names the server the tools act on, so a model knows which
	// host a change reaches. Empty for an unnamed remote.
	Destination string
	// Version is reported to the client as the server version.
	Version string
	// Now is the clock that decides what is muted, held by a maintenance
	// window or stale. Nil means time.Now.
	Now func() time.Time
}

// New returns a server exposing every up tool over b.
func New(b Backend, opts Options) *sdk.Server {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	s := sdk.NewServer(
		&sdk.Implementation{
			Name:        "up",
			Title:       "up",
			Description: "Self-hosted HTTP uptime monitor",
			Version:     version,
		},
		&sdk.ServerOptions{Instructions: instructions(opts.Destination)},
	)
	addReadTools(s, b, now)
	addMonitorTools(s, b, now)
	addAlertTools(s, b, now)
	return s
}

// instructions is the text a client shows its model once, before the tools.
// It states the rules the tools assume instead of repeating them in every
// description: which server the calls reach, where the state words come from
// and what happens to a secret that arrives as a tool argument.
func instructions(destination string) string {
	where := "the up server saved by 'up login'"
	if destination != "" {
		where = destination
	}
	return "up monitors HTTP, TCP, ping, DNS and heartbeat checks and alerts on them. " +
		"Every tool reads and writes " + where + " through its authenticated API, so it needs " +
		"'up serve --api-listen' running on that host.\n\n" +
		"Status words are the ones the terminal UI shows (UP, DOWN, PENDING, WAITING, " +
		"FAILING n/N, RECOVERING n/M); an incident is an outage with what became of its alerts.\n\n" +
		"Durations are Go duration strings such as 30s, 5m or 1h30m. A mute lasts between 1m and " +
		"168h (7 days); a maintenance window lasts between 1m and 168h too, and repeats weekly in its zone.\n\n" +
		"Sealed values (header values, request bodies, SMTP passwords, webhook URLs and auth " +
		"values, push tokens) are never returned: a tool reports set or empty instead. A write " +
		"takes one as 'edit: replace' with the value, or 'keep' to leave the stored one alone. " +
		"A secret passed as a tool argument travels through the model and the client's logs; " +
		"setting it in 'up tui' keeps it out of both. A heartbeat ping token is a credential too: " +
		"it records pings for one monitor and grants nothing else.\n\n" +
		"Writes are validated by the server and a rejected write changes nothing. Deleting a " +
		"monitor drops its history and cannot be undone."
}
