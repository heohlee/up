package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime/debug"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/heohlee/up/internal/client"
	"github.com/heohlee/up/internal/connection"
	"github.com/heohlee/up/internal/mcp"
)

const mcpHelp = `Usage: up mcp

Serve the Model Context Protocol on stdin and stdout, so an agent client such as
Pi, Claude Desktop or Cursor can read and manage an up server with tools.

It connects to the remote saved by 'up login' and needs 'up serve --api-listen'
running on that host. It never prompts: stdin carries the protocol, so the
credentials must already be saved. Register it with the client, for example:

  pi mcp add up -- up mcp

Run 'up login --url https://up.example.com' first if no remote is saved.
Tools change what you ask them to change; deleting a monitor or a channel needs
an explicit confirm. Sealed values (header values, request bodies, passwords,
webhook URLs, tokens) are never returned, and a secret passed as a tool
argument travels through the model and the client's logs.
`

// mcpTransport is the process boundary: newline-delimited JSON over the real
// stdin and stdout. CLI tests replace it to speak the protocol over pipes.
var mcpTransport = func() sdk.Transport { return &sdk.StdioTransport{} }

// runMCP serves the protocol on stdout and reports every diagnostic on stderr,
// because stdout is the JSON-RPC channel.
func runMCP(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	if code, ok := parseFlags(fs, mcpHelp, args, stdout, stderr); !ok {
		return code
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "up mcp: unexpected argument %q\n\n%s", fs.Arg(0), mcpHelp)
		return 2
	}
	profile, err := connection.LoadProfile()
	if errors.Is(err, connection.ErrNoProfile) {
		fmt.Fprintln(stderr, "up: no saved remote; run 'up login'")
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	server := mcp.New(client.New(profile.ServerURL, profile.Token), mcp.Options{
		Destination: profile.ServerURL,
		Version:     buildVersion(),
	})
	if err := server.Run(ctx, mcpTransport()); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	return 0
}

// buildVersion is the version reported to the MCP client: the module version
// of a 'go install ...@vX.Y.Z' binary, empty for a local build.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return ""
	}
	return info.Main.Version
}
