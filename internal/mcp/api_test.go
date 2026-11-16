package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/client"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/remote"
	"github.com/heohlee/up/internal/secret"
	"github.com/heohlee/up/internal/store"
	"github.com/heohlee/up/internal/tui"
)

const testToken = "up_" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCdE"

// fixedNow is the instant the tests arrange data around. It is one reading of
// the real clock, because the remote client shifts every server time into the
// caller's clock: a fake instant here would make every timestamp meaningless.
var fixedNow = time.Now()

// api is a real up server: a temporary SQLite database with its encryption
// key and the production API handlers behind httptest, reached through the same
// remote client the binary uses. Nothing is faked, so a tool call runs the
// whole path a shipped `up mcp` runs: protocol, schema, HTTP, API, store.
type api struct {
	t     *testing.T
	store *store.Store
	key   *secret.Key
	http  *httptest.Server
}

func newAPI(t *testing.T) *api {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "up.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	key, _, err := secret.LoadOrCreate(filepath.Join(dir, "up.db.key"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.SetPublicKey(ctx, key.PublicKey()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAPIListener(ctx, config.APIListener{Address: "127.0.0.1:8080"}); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(dir, "up.db.token")
	if err := remote.SaveToken(tokenPath, testToken); err != nil {
		t.Fatal(err)
	}
	verifier, err := remote.LoadVerifier(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	a := &api{t: t, store: s, key: key}
	a.http = httptest.NewServer(remote.NewHandler(remote.Options{Verifier: verifier, Version: "test", Routes: a.routes()}))
	t.Cleanup(a.http.Close)
	return a
}

// routes mirrors cmd/up/api.go, so every route the binary serves is served
// here and a tool cannot pass against a route the daemon lacks.
func (a *api) routes() http.Handler {
	routes := http.NewServeMux()
	routes.Handle("/api/v1/snapshot", remote.SnapshotRoutes(func(ctx context.Context, now time.Time) (tui.Snapshot, error) {
		return tui.Load(ctx, a.store, now)
	}))
	writes := remote.WriteRoutes(a.store)
	routes.Handle("/api/v1/checks", writes)
	routes.Handle("/api/v1/checks/", writes)
	alerts := remote.AlertWriteRoutes(a.store, a.key)
	for _, path := range []string{"/api/v1/smtp-servers", "/api/v1/smtp-servers/", "/api/v1/channels", "/api/v1/channels/"} {
		routes.Handle(path, alerts)
	}
	maintenance := remote.MaintenanceRoutes(a.store)
	routes.Handle("/api/v1/checks/{name}/maintenance", maintenance)
	routes.Handle("/api/v1/checks/{name}/maintenance/{id}", maintenance)
	tags := remote.TagRoutes(a.store)
	for _, p := range remote.TagPatterns {
		routes.Handle(p, tags)
	}
	routes.Handle("/api/v1/defaults", remote.DefaultsRoutes(a.store))
	routes.Handle("/api/v1/deliveries", remote.DeliveryRoutes(a.store))
	routes.Handle("/api/v1/channels/{name}/test", remote.ChannelTestRoutes(a.store))
	routes.Handle("/api/v1/checks/{name}/heartbeat-token", remote.HeartbeatTokenRoutes(a.store, a.key))
	return routes
}

// session connects a real MCP client to the server over the SDK's in-memory
// transport, so every test goes through the protocol and its JSON schemas.
func (a *api) session() *sdk.ClientSession {
	a.t.Helper()
	server := New(client.New(a.http.URL, testToken), Options{
		Destination: "https://up.test",
		Version:     "test",
		Now:         func() time.Time { return fixedNow },
	})
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		a.t.Fatal(err)
	}
	a.t.Cleanup(func() { ss.Close() })
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		a.t.Fatal(err)
	}
	a.t.Cleanup(func() { cs.Close() })
	return cs
}

// callTool runs one tool and decodes its structured output. A tool error comes
// back as an error with the text the model would read; a protocol failure fails
// the test at once.
func callTool[Out any](t *testing.T, s *sdk.ClientSession, tool string, args any) (Out, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if args == nil {
		args = struct{}{}
	}
	res, err := s.CallTool(ctx, &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", tool, err)
	}
	var out Out
	if raw, err := json.Marshal(res.StructuredContent); err == nil && len(raw) > 4 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s: decode %s: %v", tool, raw, err)
		}
	}
	if res.IsError {
		return out, errors.New(toolText(res))
	}
	return out, nil
}

func mustTool[Out any](t *testing.T, s *sdk.ClientSession, tool string, args any) Out {
	t.Helper()
	out, err := callTool[Out](t, s, tool, args)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return out
}

func mustFail(t *testing.T, s *sdk.ClientSession, tool string, args any) string {
	t.Helper()
	_, err := callTool[map[string]any](t, s, tool, args)
	if err == nil {
		t.Fatalf("%s succeeded; want an error", tool)
	}
	return err.Error()
}

// toolText is the text content of a result, which is what a model reads.
func toolText(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if text, ok := c.(*sdk.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

// jsonOf is the whole result as JSON, for the tests that prove a secret never
// appears in what a model receives.
func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// addCheck stores a monitor the way the daemon would find it.
func (a *api) addCheck(c config.Check) {
	a.t.Helper()
	if err := a.store.AddCheck(context.Background(), c); err != nil {
		a.t.Fatal(err)
	}
}

// record stores one check result.
func (a *api) record(name string, at time.Time, status check.Status, reason string) {
	a.t.Helper()
	r := check.Result{CheckName: name, At: at, Status: status, Latency: 42 * time.Millisecond, StatusCode: 200, Error: reason}
	if err := a.store.Record(context.Background(), r); err != nil {
		a.t.Fatal(err)
	}
}

func (a *api) recordAttempt(attempt store.Attempt) {
	a.t.Helper()
	if err := a.store.RecordAttempt(context.Background(), attempt); err != nil {
		a.t.Fatal(err)
	}
}

func httpCheck(name, url string) config.Check {
	return config.Check{Name: name, URL: url, Interval: time.Minute, Timeout: 5 * time.Second, ExpectedStatus: 200}
}

// seeded is a server with two monitors, history and a delivery, the state most
// read tests start from.
func seeded(t *testing.T) (*api, *sdk.ClientSession) {
	t.Helper()
	a := newAPI(t)
	a.addCheck(httpCheck("api", "https://api.example.com/health"))
	a.addCheck(httpCheck("site", "https://example.com"))
	a.record("api", fixedNow.Add(-30*time.Second), check.StatusUp, "")
	a.record("site", fixedNow.Add(-2*time.Minute), check.StatusDown, "status 500")
	a.recordAttempt(store.Attempt{At: fixedNow.Add(-time.Minute), Monitor: "site", Channel: "ops", Event: store.EventDown, Outcome: store.OutcomeSent, Reason: "status 500"})
	return a, a.session()
}

// seal encrypts a value to the test server's key, the way the client seals a
// secret a user typed.
func (a *api) seal(t *testing.T, plaintext string) secret.Sealed {
	t.Helper()
	s, err := secret.Seal(a.key.PublicKey(), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Helpers shared by the write tests: they read what the server stored, which is
// the only way to prove a sealed value survived a write.
func ptr[T any](v T) *T { return &v }

// storedCheck is the monitor the server holds, read straight from the store so
// a test proves what a tool wrote and not only what a view reads back.
func (a *api) storedCheck(name string) config.Check {
	a.t.Helper()
	checks, err := a.store.Checks(context.Background())
	if err != nil {
		a.t.Fatal(err)
	}
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	a.t.Fatalf("no stored monitor named %q", name)
	return config.Check{}
}

// storedChannel is the channel the server holds, with its sealed values.
func (a *api) storedChannel(name string) (config.Channel, bool) {
	a.t.Helper()
	c, ok, err := a.store.Channel(context.Background(), name)
	if err != nil {
		a.t.Fatal(err)
	}
	return c, ok
}

// addWebhookChannel stores a webhook channel sealed to the test server's key,
// the way a channel created in the terminal UI is stored.
func (a *api) addWebhookChannel(name, url string) {
	a.t.Helper()
	origin, err := config.WebhookOrigin(url)
	if err != nil {
		a.t.Fatal(err)
	}
	if err := a.store.AddChannel(context.Background(), config.Channel{
		Name: name, Kind: config.KindWebhook, WebhookOrigin: origin,
		WebhookURL: a.seal(a.t, url), WebhookURLEdit: config.EditKeep,
	}); err != nil {
		a.t.Fatal(err)
	}
}

// sameSealed reports whether two sealed values are the same ciphertext.
func sameSealed(x, y secret.Sealed) bool {
	return bytes.Equal(x.Key, y.Key) && bytes.Equal(x.Data, y.Data)
}

// detailOf is one monitor read through get_monitor.
func detailOf(t *testing.T, s *sdk.ClientSession, name string) monitorDetail {
	t.Helper()
	return mustTool[getMonitorOutput](t, s, "get_monitor", monitorNameInput{Name: name}).Monitor
}

// rowsOf is the list_monitors rows keyed by name.
func rowsOf(t *testing.T, s *sdk.ClientSession) map[string]monitorView {
	t.Helper()
	out := mustTool[listMonitorsOutput](t, s, "list_monitors", listMonitorsInput{})
	rows := make(map[string]monitorView, out.Count)
	for _, row := range out.Monitors {
		rows[row.Name] = row
	}
	return rows
}

// channelNamed is one channel read through list_channels.
func channelNamed(t *testing.T, s *sdk.ClientSession, name string) (channelView, bool) {
	t.Helper()
	for _, c := range mustTool[listChannelsOutput](t, s, "list_channels", nil).Channels {
		if c.Name == name {
			return c, true
		}
	}
	return channelView{}, false
}

// TestCreateMonitorUsesTheServerDefaults proves a create needs only a name and
// a target: everything else is what set_defaults and the defaults stored by
