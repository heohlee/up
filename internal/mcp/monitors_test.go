package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
)

// The tests below cover the monitor write tools against the same real server
// the read tests use: a temporary store, the production API handlers and a real
// MCP session. Each one proves what a model observes (the tool's answer and the
// next read) and, where it matters, what the server actually stored.

func TestCreateMonitorUsesTheServerDefaults(t *testing.T) {
	a := newAPI(t)
	ctx := context.Background()
	a.addWebhookChannel("ops", "https://hooks.example.com/services/default")
	d := config.BuiltinDefaults()
	d.Interval, d.Timeout, d.ExpectedStatus = 2*time.Minute, 10*time.Second, 201
	d.Channel, d.AlertAfter, d.RecoverAfter = "ops", 3, 2
	if err := a.store.SetDefaults(ctx, d); err != nil {
		t.Fatal(err)
	}
	s := a.session()

	mustTool[writeResult](t, s, "create_monitor", createMonitorInput{Name: "api", Monitor: monitorSpec{URL: ptr("https://api.example.com/health")}})
	if got := a.storedCheck("api"); got.Interval != d.Interval || got.Timeout != d.Timeout || got.ExpectedStatus != d.ExpectedStatus ||
		got.Channel != d.Channel || got.AlertAfter != d.AlertAfter || got.RecoverAfter != d.RecoverAfter {
		t.Errorf("stored monitor = %+v, want the server defaults %+v", got, d)
	}
	cfg := detailOf(t, s, "api").Config
	if cfg.Interval != "2m0s" || cfg.Timeout != "10s" || cfg.ExpectedStatus != 201 || cfg.Channel != "ops" || cfg.AlertAfter != 3 || cfg.RecoverAfter != 2 {
		t.Errorf("created config = %+v", cfg)
	}

	reason := mustFail(t, s, "create_monitor", createMonitorInput{Name: "bad", Monitor: monitorSpec{URL: ptr("https://bad.example.com"), Interval: ptr("5")}})
	if !strings.Contains(reason, "interval must be a duration such as 30s, 5m or 1h30m") {
		t.Errorf("bad interval reason = %q", reason)
	}
	if rows := rowsOf(t, s); len(rows) != 1 {
		t.Errorf("a rejected create stored a monitor: %+v", rows)
	} else if _, ok := rows["bad"]; ok {
		t.Error("the monitor of a rejected create exists")
	}
}

// TestCreateMonitorSealsAReplacedHeader is the secret contract of a write: the
// value reaches the server sealed and never comes back, only "set" does.
func TestCreateMonitorSealsAReplacedHeader(t *testing.T) {
	a := newAPI(t)
	s := a.session()
	const value = "Bearer s3cr3t-header-value"

	mustTool[writeResult](t, s, "create_monitor", createMonitorInput{Name: "api", Monitor: monitorSpec{
		URL:     ptr("https://api.example.com"),
		Headers: []headerSpec{{Name: "Authorization", Value: value, Edit: "replace"}},
	}})
	stored := a.storedCheck("api")
	if len(stored.Headers) != 1 || stored.Headers[0].Name != "Authorization" || stored.Headers[0].Value.Empty() {
		t.Fatalf("stored headers = %+v", stored.Headers)
	}
	if strings.Contains(string(stored.Headers[0].Value.Data), value) {
		t.Error("the header value was stored in the clear")
	}
	opened, err := a.key.Open(stored.Headers[0].Value)
	if err != nil || opened != value {
		t.Fatalf("stored header does not open to the value: %q, %v", opened, err)
	}

	detail := mustTool[getMonitorOutput](t, s, "get_monitor", monitorNameInput{Name: "api"})
	if len(detail.Monitor.Config.Headers) != 1 || detail.Monitor.Config.Headers[0].Value != "set" {
		t.Fatalf("headers read as %+v", detail.Monitor.Config.Headers)
	}
	list := mustTool[listMonitorsOutput](t, s, "list_monitors", listMonitorsInput{})
	for name, body := range map[string]string{"list_monitors": jsonOf(t, list), "get_monitor": jsonOf(t, detail)} {
		if strings.Contains(body, value) {
			t.Errorf("%s returns the sealed header value: %s", name, body)
		}
	}
}

// TestUpdateMonitorPatchesOnlyWhatItSends proves an update starts from what is
// stored: an untouched header keeps its sealed value, an empty expect_text
// clears it, and the name never changes.
func TestUpdateMonitorPatchesOnlyWhatItSends(t *testing.T) {
	a := newAPI(t)
	s := a.session()
	mustTool[writeResult](t, s, "create_monitor", createMonitorInput{Name: "api", Monitor: monitorSpec{
		URL:            ptr("https://api.example.com"),
		ExpectedStatus: ptr(204),
		ExpectText:     ptr("ready"),
		Headers:        []headerSpec{{Name: "Authorization", Value: "Bearer first", Edit: "replace"}},
	}})
	before := a.storedCheck("api")

	mustTool[writeResult](t, s, "update_monitor", updateMonitorInput{Name: "api", Monitor: monitorSpec{Interval: ptr("45s")}})
	cfg := detailOf(t, s, "api").Config
	if cfg.Interval != "45s" {
		t.Errorf("interval = %q", cfg.Interval)
	}
	if cfg.URL != "https://api.example.com" || cfg.ExpectedStatus != 204 || cfg.ExpectText != "ready" {
		t.Errorf("an interval-only update changed other fields: %+v", cfg)
	}
	if len(cfg.Headers) != 1 || cfg.Headers[0].Value != "set" {
		t.Errorf("headers after the update = %+v", cfg.Headers)
	}
	after := a.storedCheck("api")
	if len(after.Headers) != 1 || !sameSealed(before.Headers[0].Value, after.Headers[0].Value) {
		t.Error("the stored header value changed on an update that did not mention it")
	}

	mustTool[writeResult](t, s, "update_monitor", updateMonitorInput{Name: "api", Monitor: monitorSpec{ExpectText: ptr("")}})
	if cfg := detailOf(t, s, "api").Config; cfg.ExpectText != "" {
		t.Errorf("expect_text after clearing = %q", cfg.ExpectText)
	}
	if out := mustTool[listMonitorsOutput](t, s, "list_monitors", listMonitorsInput{}); out.Count != 1 || out.Monitors[0].Name != "api" {
		t.Errorf("monitors after updates = %+v", out.Monitors)
	}
	reason := mustFail(t, s, "get_monitor", monitorNameInput{Name: "renamed"})
	if !strings.Contains(reason, `no monitor named "renamed"`) {
		t.Errorf("a monitor was renamed: %q", reason)
	}
}

// TestDeleteMonitorNeedsConfirm proves the confirm rule and that a refused
// delete keeps the monitor.
func TestDeleteMonitorNeedsConfirm(t *testing.T) {
	a := newAPI(t)
	s := a.session()
	mustTool[writeResult](t, s, "create_monitor", createMonitorInput{Name: "api", Monitor: monitorSpec{URL: ptr("https://api.example.com")}})

	reason := mustFail(t, s, "delete_monitor", deleteMonitorInput{Name: "api"})
	if !strings.Contains(reason, "needs confirm true") {
		t.Errorf("unconfirmed delete reason = %q", reason)
	}
	if rows := rowsOf(t, s); len(rows) != 1 {
		t.Fatalf("the monitor is gone after a refused delete: %+v", rows)
	}

	mustTool[writeResult](t, s, "delete_monitor", deleteMonitorInput{Name: "api", Confirm: true})
	if rows := rowsOf(t, s); len(rows) != 0 {
		t.Errorf("the monitor survived a confirmed delete: %+v", rows)
	}
}

// TestMuteMonitorHoldsAndReleasesAlerts proves the mute bounds, the held state
// a model reads and that a rejected duration changes nothing.
func TestMuteMonitorHoldsAndReleasesAlerts(t *testing.T) {
	a := newAPI(t)
	a.addCheck(httpCheck("api", "https://api.example.com"))
	s := a.session()

	for _, duration := range []string{"30s", "200h"} {
		reason := mustFail(t, s, "mute_monitor", muteInput{Name: "api", Duration: duration})
		if !strings.Contains(reason, "between 1m and 168h") {
			t.Errorf("mute %s reason = %q", duration, reason)
		}
		if row := rowsOf(t, s)["api"]; row.Held != "" || row.HeldUntil != "" {
			t.Errorf("a rejected mute of %s held the monitor: %+v", duration, row)
		}
	}

	mustTool[writeResult](t, s, "mute_monitor", muteInput{Name: "api", Duration: "2h"})
	row := rowsOf(t, s)["api"]
	if row.Held != "muted" || row.HeldUntil == "" {
		t.Fatalf("muted row = %+v", row)
	}
	until, err := time.Parse(time.RFC3339, row.HeldUntil)
	if err != nil {
		t.Fatalf("held_until %q: %v", row.HeldUntil, err)
	}
	if !until.After(fixedNow) || until.After(fixedNow.Add(3*time.Hour)) {
		t.Errorf("held_until %s is not about two hours from now (%s)", until, fixedNow)
	}

	mustTool[writeResult](t, s, "unmute_monitor", monitorNameInput{Name: "api"})
	if row := rowsOf(t, s)["api"]; row.Held != "" || row.HeldUntil != "" {
		t.Errorf("after unmute = %+v", row)
	}
}

// TestTagMuteHoldsEveryTaggedMonitor proves a tag's mute reaches every monitor
// carrying it and that unmuting the tag releases them together.
func TestTagMuteHoldsEveryTaggedMonitor(t *testing.T) {
	a := newAPI(t)
	for _, name := range []string{"api", "site"} {
		c := httpCheck(name, "https://"+name+".example.com")
		c.Tags = []string{"home"}
		a.addCheck(c)
	}
	s := a.session()

	mustTool[writeResult](t, s, "mute_tag", muteTagInput{Tag: "home", Duration: "1h"})
	rows := rowsOf(t, s)
	for _, name := range []string{"api", "site"} {
		if row := rows[name]; row.Held != "muted" || row.HeldUntil == "" {
			t.Errorf("%s after mute_tag = %+v", name, row)
		}
	}

	mustTool[writeResult](t, s, "unmute_tag", tagInput{Tag: "home"})
	rows = rowsOf(t, s)
	for _, name := range []string{"api", "site"} {
		if row := rows[name]; row.Held != "" || row.HeldUntil != "" {
			t.Errorf("%s after unmute_tag = %+v", name, row)
		}
	}
}

// TestMaintenanceWindowLifecycle proves a window is added with an id, read
// back with the monitor, changed, deleted, and that an invalid one stores
// nothing.
func TestMaintenanceWindowLifecycle(t *testing.T) {
	a := newAPI(t)
	a.addCheck(httpCheck("api", "https://api.example.com"))
	s := a.session()

	invalid := []struct {
		reason string
		window maintenanceSpec
	}{
		{`zone "Mars/Base" is not an IANA time zone`, maintenanceSpec{Weekdays: []string{"mon"}, Start: "02:30", Duration: "1h", Zone: "Mars/Base"}},
		{"duration must be between 1m and 7d", maintenanceSpec{Weekdays: []string{"mon"}, Start: "02:30", Duration: "169h", Zone: "UTC"}},
		{`"funday" is not a day such as mon or tuesday`, maintenanceSpec{Weekdays: []string{"funday"}, Start: "02:30", Duration: "1h", Zone: "UTC"}},
	}
	for _, c := range invalid {
		reason := mustFail(t, s, "add_maintenance_window", addWindowInput{Name: "api", Window: c.window})
		if !strings.Contains(reason, c.reason) {
			t.Errorf("invalid window %+v reason = %q, want %q", c.window, reason, c.reason)
		}
	}
	if got := detailOf(t, s, "api").Maintenance; len(got) != 0 {
		t.Fatalf("a rejected window was added: %+v", got)
	}

	added := mustTool[windowView](t, s, "add_maintenance_window", addWindowInput{Name: "api", Window: maintenanceSpec{
		Weekdays: []string{"mon"}, Start: "02:30", Duration: "1h", Zone: "UTC",
	}})
	if added.ID == 0 || added.Check != "api" || added.Start != "02:30" || added.Duration != "1h0m0s" || added.Zone != "UTC" ||
		len(added.Weekdays) != 1 || added.Weekdays[0] != "Mon" {
		t.Fatalf("added window = %+v", added)
	}
	if got := detailOf(t, s, "api").Maintenance; len(got) != 1 || got[0].ID != added.ID || got[0].Start != "02:30" {
		t.Fatalf("maintenance after add = %+v", got)
	}

	updated := mustTool[windowView](t, s, "update_maintenance_window", updateWindowInput{ID: added.ID, Name: "api", Window: maintenanceSpec{
		Weekdays: []string{"mon"}, Start: "03:15", Duration: "1h", Zone: "UTC",
	}})
	if updated.ID != added.ID || updated.Start != "03:15" {
		t.Errorf("updated window = %+v", updated)
	}
	if got := detailOf(t, s, "api").Maintenance; len(got) != 1 || got[0].ID != added.ID || got[0].Start != "03:15" {
		t.Errorf("maintenance after update = %+v", got)
	}

	mustTool[writeResult](t, s, "delete_maintenance_window", deleteWindowInput{ID: added.ID, Name: "api"})
	if got := detailOf(t, s, "api").Maintenance; len(got) != 0 {
		t.Errorf("maintenance after delete = %+v", got)
	}
}

// TestChannelLifecycleSealsTheWebhook proves a webhook channel keeps its path
// out of every answer, survives an update that keeps the stored URL, and only
// deletes with confirm, and only when no monitor uses it.

func TestMaintenanceWindowOfATag(t *testing.T) {
	a := newAPI(t)
	for _, name := range []string{"nas-a", "nas-b"} {
		c := httpCheck(name, "https://"+name+".example.com")
		c.Tags = []string{"home"}
		a.addCheck(c)
	}
	a.addCheck(httpCheck("elsewhere", "https://elsewhere.example.com"))
	s := a.session()

	window := maintenanceSpec{Weekdays: []string{"mon"}, Start: "02:00", Duration: "1h", Zone: "UTC"}
	added := mustTool[windowView](t, s, "add_maintenance_window", addWindowInput{Tag: "home", Window: window})
	if added.Tag != "home" || added.ID == 0 {
		t.Fatalf("added = %+v", added)
	}
	covers := func(want int) {
		t.Helper()
		for _, name := range []string{"nas-a", "nas-b"} {
			detail := mustTool[getMonitorOutput](t, s, "get_monitor", monitorNameInput{Name: name})
			if got := len(detail.Monitor.Maintenance); got != want {
				t.Errorf("%s holds %d windows, want %d", name, got, want)
			}
		}
		detail := mustTool[getMonitorOutput](t, s, "get_monitor", monitorNameInput{Name: "elsewhere"})
		if len(detail.Monitor.Maintenance) != 0 {
			t.Errorf("a monitor without the tag holds %+v", detail.Monitor.Maintenance)
		}
	}
	covers(1)

	updated := mustTool[windowView](t, s, "update_maintenance_window", updateWindowInput{Tag: "home", ID: added.ID, Window: maintenanceSpec{Weekdays: []string{"tue"}, Start: "03:30", Duration: "2h", Zone: "UTC"}})
	if updated.Start != "03:30" || updated.Duration != "2h0m0s" {
		t.Fatalf("updated = %+v", updated)
	}
	covers(1)

	if _, err := callTool[writeResult](t, s, "delete_maintenance_window", deleteWindowInput{Tag: "home", ID: added.ID}); err != nil {
		t.Fatalf("delete through the tag: %v", err)
	}
	covers(0)

	for _, in := range []addWindowInput{
		{Tag: "home", Name: "nas-a", Window: window},
		{Window: window},
	} {
		if _, err := callTool[windowView](t, s, "add_maintenance_window", in); err == nil {
			t.Errorf("add with name %q and tag %q was accepted; a window needs exactly one owner", in.Name, in.Tag)
		}
	}
}

// TestATagWindowIsNotReachableThroughAMonitor pins the owner rule. The store
// deletes and updates a tag's window through any monitor carrying the tag, so
// naming only a monitor must be refused: the hold belongs to monitors the
// caller never mentioned.
func TestATagWindowIsNotReachableThroughAMonitor(t *testing.T) {
	a := newAPI(t)
	for _, name := range []string{"web", "api"} {
		c := httpCheck(name, "https://"+name+".example.com")
		c.Tags = []string{"prod"}
		a.addCheck(c)
	}
	s := a.session()
	window := maintenanceSpec{Weekdays: []string{"mon"}, Start: "01:00", Duration: "1h", Zone: "UTC"}
	shared := mustTool[windowView](t, s, "add_maintenance_window", addWindowInput{Tag: "prod", Window: window})
	own := mustTool[windowView](t, s, "add_maintenance_window", addWindowInput{Name: "web", Window: window})

	holds := func(name string, want int) {
		t.Helper()
		detail := mustTool[getMonitorOutput](t, s, "get_monitor", monitorNameInput{Name: name})
		if got := len(detail.Monitor.Maintenance); got != want {
			t.Errorf("%s holds %d windows, want %d: %+v", name, got, want, detail.Monitor.Maintenance)
		}
	}

	// Naming the monitor must not reach the tag's window.
	reason := mustFail(t, s, "delete_maintenance_window", deleteWindowInput{Name: "web", ID: shared.ID})
	if !strings.Contains(reason, "#prod") {
		t.Errorf("refusal does not name the tag: %q", reason)
	}
	holds("web", 2)
	holds("api", 1)
	mustFail(t, s, "update_maintenance_window", updateWindowInput{Name: "web", ID: shared.ID, Window: window})
	holds("web", 2)

	// Naming the tag must not reach the monitor's own window.
	mustFail(t, s, "delete_maintenance_window", deleteWindowInput{Tag: "prod", ID: own.ID})
	holds("web", 2)

	// The right owner works, and only that window goes.
	if _, err := callTool[writeResult](t, s, "delete_maintenance_window", deleteWindowInput{Tag: "prod", ID: shared.ID}); err != nil {
		t.Fatalf("delete the tag window: %v", err)
	}
	holds("web", 1)
	holds("api", 0)
	if _, err := callTool[writeResult](t, s, "delete_maintenance_window", deleteWindowInput{Name: "web", ID: own.ID}); err != nil {
		t.Fatalf("delete the monitor window: %v", err)
	}
	holds("web", 0)
}

// TestAnUnknownWindowFails proves a window id that no owner has is refused
// instead of reaching the store.
func TestAnUnknownWindowFails(t *testing.T) {
	a := newAPI(t)
	a.addCheck(httpCheck("web", "https://web.example.com"))
	s := a.session()
	window := maintenanceSpec{Weekdays: []string{"mon"}, Start: "01:00", Duration: "1h", Zone: "UTC"}
	if reason := mustFail(t, s, "delete_maintenance_window", deleteWindowInput{Name: "web", ID: 99}); !strings.Contains(reason, "no maintenance window 99") {
		t.Errorf("reason = %q", reason)
	}
	if reason := mustFail(t, s, "update_maintenance_window", updateWindowInput{Name: "web", ID: 99, Window: window}); !strings.Contains(reason, "no maintenance window 99") {
		t.Errorf("reason = %q", reason)
	}
}
