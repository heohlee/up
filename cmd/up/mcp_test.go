package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/heohlee/up/internal/connection"
)

// speak runs `up mcp` over pipes and returns the frames it answered plus its
// exit code. The requests go first and the answers are read before the stream
// closes, so the session ends when the exchange is over, not before.
func speak(t *testing.T, requests []string, want int) ([]map[string]any, int) {
	t.Helper()
	reqR, reqW := io.Pipe()
	resR, resW := io.Pipe()
	previous := mcpTransport
	mcpTransport = func() sdk.Transport { return &sdk.IOTransport{Reader: reqR, Writer: resW} }
	t.Cleanup(func() {
		mcpTransport = previous
		reqW.Close()
	})

	done := make(chan int, 1)
	go func() {
		var stderr bytes.Buffer
		code := runMCP(context.Background(), nil, io.Discard, &stderr)
		done <- code
	}()
	go func() {
		for _, request := range requests {
			if _, err := io.WriteString(reqW, request+"\n"); err != nil {
				return
			}
		}
	}()

	frames := make([]map[string]any, 0, want)
	reader := bufio.NewReader(resR)
	for len(frames) < want {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read frame %d: %v", len(frames)+1, err)
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("stdout frame is not JSON-RPC: %q (%v)", line, err)
		}
		frames = append(frames, frame)
	}
	reqW.Close()
	select {
	case code := <-done:
		return frames, code
	case <-time.After(10 * time.Second):
		t.Fatal("up mcp did not exit after its input closed")
		return nil, 0
	}
}

// TestMCPWithoutProfile proves the subcommand fails with the sentence the user
// needs, on stderr, and never writes to the protocol channel.
func TestMCPWithoutProfile(t *testing.T) {
	isolateDefaultDatabase(t)
	code, stdout, stderr := runCmd(context.Background(), "mcp")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "no saved remote; run 'up login'") {
		t.Errorf("stderr = %q", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout carried %q; the protocol owns it", stdout)
	}
}

func TestMCPHelpAndArguments(t *testing.T) {
	isolateDefaultDatabase(t)
	code, stdout, stderr := runCmd(context.Background(), "mcp", "-h")
	if code != 0 || !strings.Contains(stdout, "Usage: up mcp") || stderr != "" {
		t.Errorf("up mcp -h = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	code, stdout, stderr = runCmd(context.Background(), "mcp", "extra")
	if code != 2 || stdout != "" || !strings.Contains(stderr, `unexpected argument "extra"`) {
		t.Errorf("up mcp extra = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// TestMCPServesTheProtocol runs the real subcommand: it loads the profile,
// names the saved remote and answers initialize and tools/list as JSON-RPC
// frames, nothing else reaching that stream.
func TestMCPServesTheProtocol(t *testing.T) {
	isolateDefaultDatabase(t)
	if err := connection.SaveProfile(connection.Profile{ServerURL: "https://up.example.com", Token: "up_" + "token"}); err != nil {
		t.Fatal(err)
	}
	frames, code := speak(t, []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}, 2)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	init, _ := frames[0]["result"].(map[string]any)
	info, _ := init["serverInfo"].(map[string]any)
	if info["name"] != "up" {
		t.Errorf("serverInfo = %+v", info)
	}
	if instructions, _ := init["instructions"].(string); !strings.Contains(instructions, "https://up.example.com") {
		t.Errorf("instructions do not name the destination: %q", instructions)
	}
	list, _ := frames[1]["result"].(map[string]any)
	tools, _ := list["tools"].([]any)
	if len(tools) != 27 {
		t.Errorf("tools = %d, want the 27 the feature document lists", len(tools))
	}
}

// TestMCPToolErrorReachesTheClient proves a dead server reaches the model as a
// tool error inside the protocol, not as a process that fails to start: the
// subcommand still answers, so the client can report it and recover later.
func TestMCPToolErrorReachesTheClient(t *testing.T) {
	isolateDefaultDatabase(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	if err := connection.SaveProfile(connection.Profile{ServerURL: "http://" + address, Token: "up_" + "token"}); err != nil {
		t.Fatal(err)
	}
	frames, code := speak(t, []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_monitors","arguments":{}}}`,
	}, 2)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	result, _ := frames[1]["result"].(map[string]any)
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatalf("a call against a dead server answered %+v", result)
	}
	raw, _ := json.Marshal(result)
	text := string(raw)
	if !strings.Contains(text, "connection refused") && !strings.Contains(text, "connection timed out") && !strings.Contains(text, "server unreachable") {
		t.Errorf("the failure reason is not in the result: %s", text)
	}
}

// TestMCPExitsOnClosedInput pins the invariant that makes the subcommand usable
// from an agent client: it never waits for input it does not have.
func TestMCPExitsOnClosedInput(t *testing.T) {
	isolateDefaultDatabase(t)
	if err := connection.SaveProfile(connection.Profile{ServerURL: "http://127.0.0.1:1", Token: "up_" + "token"}); err != nil {
		t.Fatal(err)
	}
	reqR, reqW := io.Pipe()
	prev := mcpTransport
	mcpTransport = func() sdk.Transport { return &sdk.IOTransport{Reader: reqR, Writer: nopWriteCloser{io.Discard}} }
	t.Cleanup(func() { mcpTransport = prev })
	reqW.Close()

	done := make(chan int, 1)
	go func() { done <- runMCP(context.Background(), nil, io.Discard, io.Discard) }()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code = %d, want 0 on an empty stream", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("up mcp did not exit after its input closed")
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
