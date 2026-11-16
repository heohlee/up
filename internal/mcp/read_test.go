package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
	"github.com/heohlee/up/internal/store"
)

// TestToolsAreTheDocumentedSet pins the tool surface: a tool that appears
// without its test, or a name a client would expose as mcp__up__* differently,
// is a contract change.
func TestToolsAreTheDocumentedSet(t *testing.T) {
	a := newAPI(t)
	s := a.session()
	want := []string{
		"add_maintenance_window", "create_channel", "create_monitor", "create_smtp_server",
		"daemon_status", "delete_channel", "delete_maintenance_window", "delete_monitor",
		"delete_smtp_server", "get_defaults", "get_heartbeat_token", "get_monitor",
		"list_channels", "list_deliveries", "list_monitors", "list_smtp_servers",
		"mute_monitor", "mute_tag", "rotate_heartbeat_token", "set_defaults",
		"test_channel", "unmute_monitor", "unmute_tag", "update_channel",
		"update_maintenance_window", "update_monitor", "update_smtp_server",
	}
	seen := map[string]bool{}
	for tool, err := range s.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		seen[tool.Name] = true
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
	}
	for _, name := range want {
		if !seen[name] {
			t.Errorf("tool %q is missing", name)
		}
		delete(seen, name)
	}
	for name := range seen {
		t.Errorf("tool %q is not in the documented set", name)
	}
}

// TestInstructionsNameTheDestination proves a model is told which server the
// tools reach, because a wrong host is the costliest mistake here.
func TestInstructionsNameTheDestination(t *testing.T) {
	a := newAPI(t)
	s := a.session()
	res := s.InitializeResult()
	if res == nil || !strings.Contains(res.Instructions, "https://up.test") {
		t.Fatalf("instructions do not name the destination: %+v", res)
	}
}

func TestListMonitorsShowsState(t *testing.T) {
	_, s := seeded(t)
	out := mustTool[listMonitorsOutput](t, s, "list_monitors", listMonitorsInput{})
	if out.Count != 2 || len(out.Monitors) != 2 {
		t.Fatalf("count = %d, monitors = %+v", out.Count, out.Monitors)
	}
	byName := map[string]monitorView{}
	for _, m := range out.Monitors {
		byName[m.Name] = m
	}
	if got := byName["api"]; got.Status != "UP" || got.Target != "https://api.example.com/health" || got.LatencyMs != 42 {
		t.Errorf("api = %+v", got)
	}
	if got := byName["site"]; got.Status != "DOWN" || got.Reason != "status 500" || got.DownSince == "" {
		t.Errorf("site = %+v", got)
	}
	if got := byName["api"]; got.Kind != config.CheckHTTP {
		t.Errorf("kind = %q", got.Kind)
	}
}

func TestListMonitorsFilters(t *testing.T) {
	a, s := seeded(t)
	tagged := httpCheck("nas", "https://nas.example.com")
	tagged.Tags = []string{"home"}
	a.addCheck(tagged)
	a.record("nas", fixedNow.Add(-time.Minute), check.StatusUp, "")

	if out := mustTool[listMonitorsOutput](t, s, "list_monitors", listMonitorsInput{Tag: "home"}); out.Count != 1 || out.Monitors[0].Name != "nas" {
		t.Errorf("tag filter = %+v", out)
	}
	if out := mustTool[listMonitorsOutput](t, s, "list_monitors", listMonitorsInput{State: "down"}); out.Count != 1 || out.Monitors[0].Name != "site" {
		t.Errorf("state filter = %+v", out)
	}
	if out := mustTool[listMonitorsOutput](t, s, "list_monitors", listMonitorsInput{Tag: "absent"}); out.Count != 0 {
		t.Errorf("unknown tag = %+v", out)
	}
}

func TestGetMonitorReportsDetailAndRejectsUnknown(t *testing.T) {
	_, s := seeded(t)
	out := mustTool[getMonitorOutput](t, s, "get_monitor", monitorNameInput{Name: "api"})
	if out.Monitor.Name != "api" || out.Monitor.Config.URL != "https://api.example.com/health" {
		t.Fatalf("detail = %+v", out.Monitor)
	}
	if out.Monitor.Config.Interval != "1m0s" || out.Monitor.Config.Timeout != "5s" || out.Monitor.Config.ExpectedStatus != 200 {
		t.Errorf("config = %+v", out.Monitor.Config)
	}
	if len(out.Monitor.Recent) != 1 || out.Monitor.Recent[0].Status != "up" {
		t.Errorf("recent = %+v", out.Monitor.Recent)
	}

	reason := mustFail(t, s, "get_monitor", monitorNameInput{Name: "nope"})
	if !strings.Contains(reason, `no monitor named "nope"`) {
		t.Errorf("unknown monitor reason = %q", reason)
	}
}

// TestSealedValuesAreNeverReturned is the security contract of every read
// tool: a stored secret reaches neither the structured output nor the text.
func TestSealedValuesAreNeverReturned(t *testing.T) {
	a := newAPI(t)
	c := httpCheck("api", "https://api.example.com")
	sealed, err := secret.Seal(a.key.PublicKey(), "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	c.Headers = []config.Header{{Name: "Authorization", Value: sealed, Edit: config.EditKeep}}
	c.Body = sealed
	c.BodyEdit = config.EditKeep
	a.addCheck(c)
	sealedChannel, err := secret.Seal(a.key.PublicKey(), "https://hooks.example.com/secret-path")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.AddChannel(context.Background(), config.Channel{
		Name: "ops", Kind: config.KindWebhook, WebhookOrigin: "https://hooks.example.com",
		WebhookURL: sealedChannel, WebhookURLEdit: config.EditKeep,
	}); err != nil {
		t.Fatal(err)
	}
	s := a.session()

	detail := mustTool[getMonitorOutput](t, s, "get_monitor", monitorNameInput{Name: "api"})
	if len(detail.Monitor.Config.Headers) != 1 || detail.Monitor.Config.Headers[0].Value != "set" || detail.Monitor.Config.Body != "set" {
		t.Fatalf("sealed values read as %+v", detail.Monitor.Config)
	}
	for _, body := range []string{jsonOf(t, detail), detail.Monitor.Config.Headers[0].Value, detail.Monitor.Config.Body} {
		if strings.Contains(body, "hunter2") || strings.Contains(body, "secret-path") {
			t.Errorf("a sealed value leaked: %s", body)
		}
	}

	channels := mustTool[listChannelsOutput](t, s, "list_channels", nil)
	if channels.Count != 1 || channels.Channels[0].WebhookURL != "set" || channels.Channels[0].WebhookOrigin != "https://hooks.example.com" {
		t.Fatalf("channel = %+v", channels.Channels)
	}
	if strings.Contains(jsonOf(t, channels), "secret-path") {
		t.Error("the webhook path leaked")
	}
}

func TestListDeliveriesFiltersAndTruncates(t *testing.T) {
	a, s := seeded(t)
	for i := range 3 {
		a.recordAttempt(store.Attempt{At: fixedNow.Add(-time.Duration(i) * time.Minute), Monitor: "api", Channel: "ops", Event: store.EventRecovered, Outcome: store.OutcomeSent})
	}

	all := mustTool[listDeliveriesOutput](t, s, "list_deliveries", listDeliveriesInput{})
	if all.Count != 4 {
		t.Fatalf("count = %d, want 4", all.Count)
	}
	if first := all.Deliveries[0]; first.Monitor != "api" || first.Event != "monitor.recovered" {
		t.Errorf("newest first: %+v", first)
	}
	byMonitor := mustTool[listDeliveriesOutput](t, s, "list_deliveries", listDeliveriesInput{Monitor: "site"})
	if byMonitor.Count != 1 || byMonitor.Deliveries[0].Outcome != "sent" {
		t.Errorf("monitor filter = %+v", byMonitor)
	}
	limited := mustTool[listDeliveriesOutput](t, s, "list_deliveries", listDeliveriesInput{Limit: 2})
	if limited.Count != 2 || !limited.Truncated {
		t.Errorf("limit = %+v", limited)
	}
	if none := mustTool[listDeliveriesOutput](t, s, "list_deliveries", listDeliveriesInput{Channel: "absent"}); none.Count != 0 {
		t.Errorf("unknown channel = %+v", none)
	}
}

func TestDaemonStatusReportsTheDaemon(t *testing.T) {
	a := newAPI(t)
	if err := a.store.Beat(context.Background(), fixedNow.Add(-20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	s := a.session()
	out := mustTool[daemonStatusOutput](t, s, "daemon_status", nil)
	// The beat is twenty minutes old, measured after however long this test
	// has taken so far. The band absorbs the truncation of two different
	// instants: the server's beat and the client's clock shift.
	elapsed := int(time.Since(fixedNow).Seconds())
	want := 20*60 + elapsed
	if !out.BeatStale || out.SecondsSinceBeat < want-5 || out.SecondsSinceBeat > want+10 {
		t.Errorf("stale beat = %+v after %ds, want about %ds", out, elapsed, want)
	}
	if !out.APIListening || !out.HasEncryptionKey {
		t.Errorf("status = %+v", out)
	}
	if out.LastBeat == "" {
		t.Error("no last beat reported")
	}
}

func TestGetDefaultsReportsTheDefaultsInUse(t *testing.T) {
	a, s := seeded(t)
	out := mustTool[getDefaultsOutput](t, s, "get_defaults", nil)
	builtin := config.BuiltinDefaults()
	if out.Defaults.Interval != builtin.Interval.String() || out.Defaults.ExpectedStatus != builtin.ExpectedStatus || out.Defaults.Zone != builtin.Zone {
		t.Fatalf("defaults = %+v, want %+v", out.Defaults, builtin)
	}

	if err := a.store.AddChannel(context.Background(), config.Channel{Name: "ops", Kind: config.KindWebhook, WebhookOrigin: "https://hooks.example.com", WebhookURL: a.seal(t, "https://hooks.example.com/x"), WebhookURLEdit: config.EditKeep}); err != nil {
		t.Fatal(err)
	}
	saved := builtin
	saved.Zone, saved.Channel, saved.Interval = "America/New_York", "ops", 2*time.Minute
	if err := a.store.SetDefaults(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	if out := mustTool[getDefaultsOutput](t, s, "get_defaults", nil); out.Defaults.Zone != "America/New_York" || out.Defaults.Interval != "2m0s" {
		t.Errorf("stored defaults = %+v", out.Defaults)
	}
}
