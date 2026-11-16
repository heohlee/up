package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
)

// The tests below cover the alert and configuration write tools: channels,
// SMTP servers, defaults and heartbeat ping tokens.

func TestChannelLifecycleSealsTheWebhook(t *testing.T) {
	a := newAPI(t)
	s := a.session()
	const url = "https://hooks.example.com/services/T000/B000/xyzzy-secret-path"

	mustTool[writeResult](t, s, "create_channel", createChannelInput{Name: "ops", Channel: channelSpec{
		Kind: ptr(config.KindWebhook), WebhookURL: ptr(url), WebhookURLEdit: ptr("replace"),
	}})
	channels := mustTool[listChannelsOutput](t, s, "list_channels", nil)
	if channels.Count != 1 {
		t.Fatalf("channels = %+v", channels.Channels)
	}
	got := channels.Channels[0]
	if got.Name != "ops" || got.Kind != config.KindWebhook || got.WebhookOrigin != "https://hooks.example.com" || got.WebhookURL != "set" {
		t.Errorf("channel = %+v", got)
	}
	if strings.Contains(jsonOf(t, channels), "xyzzy-secret-path") {
		t.Error("the webhook path leaked")
	}
	stored, ok := a.storedChannel("ops")
	if !ok || stored.WebhookURL.Empty() {
		t.Fatalf("stored channel = %+v", stored)
	}
	if opened, err := a.key.Open(stored.WebhookURL); err != nil || opened != url {
		t.Fatalf("stored webhook does not open to the URL: %q, %v", opened, err)
	}

	mustTool[writeResult](t, s, "update_channel", updateChannelInput{Name: "ops", Channel: channelSpec{WebhookURLEdit: ptr("keep")}})
	if got, _ := channelNamed(t, s, "ops"); got.WebhookURL != "set" || got.WebhookOrigin != "https://hooks.example.com" {
		t.Errorf("channel after a keep update = %+v", got)
	}
	kept, _ := a.storedChannel("ops")
	if !sameSealed(stored.WebhookURL, kept.WebhookURL) {
		t.Error("a keep update replaced the stored webhook URL")
	}

	reason := mustFail(t, s, "delete_channel", deleteChannelInput{Name: "ops"})
	if !strings.Contains(reason, "needs confirm true") {
		t.Errorf("unconfirmed delete reason = %q", reason)
	}
	if _, ok := channelNamed(t, s, "ops"); !ok {
		t.Fatal("the channel is gone after a refused delete")
	}

	mustTool[writeResult](t, s, "create_monitor", createMonitorInput{Name: "api", Monitor: monitorSpec{
		URL: ptr("https://api.example.com"), Channel: ptr("ops"),
	}})
	reason = mustFail(t, s, "delete_channel", deleteChannelInput{Name: "ops", Confirm: true})
	if !strings.Contains(reason, `"ops"`) || !strings.Contains(reason, "used by monitor api") {
		t.Errorf("deleting a used channel reason = %q", reason)
	}
	if _, ok := channelNamed(t, s, "ops"); !ok {
		t.Fatal("a channel a monitor uses was deleted")
	}

	a.addWebhookChannel("spare", "https://hooks.example.com/services/spare")
	mustTool[writeResult](t, s, "delete_channel", deleteChannelInput{Name: "spare", Confirm: true})
	if _, ok := channelNamed(t, s, "spare"); ok {
		t.Error("a confirmed delete left the channel")
	}
}

// TestTestChannelAnswersPending proves the tool enqueues the test the daemon
// sends instead of claiming it went out.
func TestTestChannelAnswersPending(t *testing.T) {
	a := newAPI(t)
	a.addWebhookChannel("ops", "https://hooks.example.com/services/ops")
	s := a.session()

	out := mustTool[testView](t, s, "test_channel", channelNameInput{Name: "ops"})
	if out.Status != string(config.TestPending) {
		t.Errorf("status = %q, want %q", out.Status, config.TestPending)
	}
	if out.Requested == "" {
		t.Error("no request time")
	}
	if got, _ := channelNamed(t, s, "ops"); got.Test == nil || got.Test.Status != string(config.TestPending) {
		t.Errorf("last test = %+v", got.Test)
	}
}

// TestSMTPServerLifecycle proves the password is stored sealed and never
// returned, that it cannot be cleared, and that a delete needs confirm.
func TestSMTPServerLifecycle(t *testing.T) {
	a := newAPI(t)
	s := a.session()
	const password = "smtp-pass-1234"

	mustTool[writeResult](t, s, "create_smtp_server", createSMTPInput{Name: "mail", Server: smtpSpec{
		Host: ptr("smtp.example.com"), Port: ptr(587), TLS: ptr("starttls"),
		Username: ptr("alerts"), Password: ptr(password), PasswordEdit: ptr("replace"),
		Sender: ptr("up <alerts@example.com>"),
	}})
	servers := mustTool[listSMTPOutput](t, s, "list_smtp_servers", nil)
	if servers.Count != 1 {
		t.Fatalf("servers = %+v", servers.Servers)
	}
	if got := servers.Servers[0]; got.Name != "mail" || got.Host != "smtp.example.com" || got.Port != 587 ||
		got.TLS != "starttls" || got.Username != "alerts" || got.Password != "set" {
		t.Errorf("server = %+v", got)
	}
	if strings.Contains(jsonOf(t, servers), password) {
		t.Error("the SMTP password leaked")
	}

	reason := mustFail(t, s, "update_smtp_server", updateSMTPInput{Name: "mail", Server: smtpSpec{PasswordEdit: ptr("clear")}})
	if !strings.Contains(reason, "cannot be cleared") {
		t.Errorf("clearing the password reason = %q", reason)
	}
	if after := mustTool[listSMTPOutput](t, s, "list_smtp_servers", nil); len(after.Servers) != 1 || after.Servers[0].Password != "set" {
		t.Errorf("the password was cleared: %+v", after.Servers)
	}

	reason = mustFail(t, s, "delete_smtp_server", deleteSMTPInput{Name: "mail"})
	if !strings.Contains(reason, "needs confirm true") {
		t.Errorf("unconfirmed delete reason = %q", reason)
	}
	if after := mustTool[listSMTPOutput](t, s, "list_smtp_servers", nil); after.Count != 1 {
		t.Fatalf("the server is gone after a refused delete: %+v", after.Servers)
	}
	mustTool[writeResult](t, s, "delete_smtp_server", deleteSMTPInput{Name: "mail", Confirm: true})
	if after := mustTool[listSMTPOutput](t, s, "list_smtp_servers", nil); after.Count != 0 {
		t.Errorf("the server survived a confirmed delete: %+v", after.Servers)
	}
}

// TestSetDefaultsPatchesOnlyWhatItSends proves a partial update keeps the rest
// and that a bad value stores nothing.
func TestSetDefaultsPatchesOnlyWhatItSends(t *testing.T) {
	a := newAPI(t)
	ctx := context.Background()
	saved := config.BuiltinDefaults()
	saved.Interval, saved.Timeout, saved.ExpectedStatus = 90*time.Second, 5*time.Second, 204
	if err := a.store.SetDefaults(ctx, saved); err != nil {
		t.Fatal(err)
	}
	s := a.session()

	out := mustTool[getDefaultsOutput](t, s, "set_defaults", setDefaultsInput{Defaults: defaultsSpec{Zone: ptr("America/New_York")}})
	if out.Defaults.Zone != "America/New_York" || out.Defaults.Interval != "1m30s" || out.Defaults.ExpectedStatus != 204 {
		t.Errorf("defaults after a zone-only update = %+v", out.Defaults)
	}
	stored, err := a.store.Defaults(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Zone != "America/New_York" || stored.Interval != 90*time.Second || stored.ExpectedStatus != 204 {
		t.Errorf("stored defaults = %+v", stored)
	}

	reason := mustFail(t, s, "set_defaults", setDefaultsInput{Defaults: defaultsSpec{Channel: ptr("absent")}})
	if !strings.Contains(reason, `channel "absent" does not exist`) {
		t.Errorf("unknown channel reason = %q", reason)
	}
	after, err := a.store.Defaults(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after != stored {
		t.Errorf("a rejected update changed the defaults: %+v, want %+v", after, stored)
	}
}

// TestHeartbeatTokenFlow proves a heartbeat can be created from a period, that
// get_heartbeat_token returns the ping credential and its path, and that
// rotating it needs confirm and invalidates the old token.
func TestHeartbeatTokenFlow(t *testing.T) {
	a := newAPI(t)
	ctx := context.Background()
	s := a.session()
	mustTool[writeResult](t, s, "create_monitor", createMonitorInput{Name: "nightly", Monitor: monitorSpec{
		Kind: ptr(config.CheckHeartbeat), Period: ptr("24h"), Grace: ptr("30m"),
	}})
	if cfg := detailOf(t, s, "nightly").Config; cfg.Kind != config.CheckHeartbeat || cfg.Period != "24h0m0s" || cfg.Grace != "30m0s" {
		t.Fatalf("heartbeat config = %+v", cfg)
	}

	first := mustTool[heartbeatTokenOutput](t, s, "get_heartbeat_token", monitorNameInput{Name: "nightly"})
	if !strings.HasPrefix(first.Token, "uphb_") {
		t.Errorf("token = %q, want a ping token", first.Token)
	}
	if first.Path != config.HeartbeatPath(first.Token) || !strings.HasPrefix(first.Path, "/api/v1/heartbeat/") {
		t.Errorf("path = %q, want the ping path of the token", first.Path)
	}
	// The failure ping is the same path plus the documented suffix, so a model
	// can build both URLs from the answer.
	if fail := first.Path + config.HeartbeatFailSuffix; !strings.HasSuffix(fail, "/fail") {
		t.Errorf("failure ping %q does not end in /fail", fail)
	}
	if first.Warning == "" {
		t.Error("the token is returned without its warning")
	}
	if known, err := a.store.HeartbeatTokenKnown(ctx, first.Token); err != nil || !known {
		t.Fatalf("the returned token is not the stored one: known=%v err=%v", known, err)
	}

	reason := mustFail(t, s, "rotate_heartbeat_token", rotateHeartbeatInput{Name: "nightly"})
	if !strings.Contains(reason, "needs confirm true") {
		t.Errorf("unconfirmed rotate reason = %q", reason)
	}
	if known, err := a.store.HeartbeatTokenKnown(ctx, first.Token); err != nil || !known {
		t.Errorf("an unconfirmed rotate replaced the token: known=%v err=%v", known, err)
	}

	second := mustTool[heartbeatTokenOutput](t, s, "rotate_heartbeat_token", rotateHeartbeatInput{Name: "nightly", Confirm: true})
	if second.Token == first.Token {
		t.Fatal("rotate returned the same token")
	}
	if second.Path != config.HeartbeatPath(second.Token) {
		t.Errorf("rotated path = %q", second.Path)
	}
	if known, err := a.store.HeartbeatTokenKnown(ctx, first.Token); err != nil || known {
		t.Errorf("the old token still matches: known=%v err=%v", known, err)
	}
	if known, err := a.store.HeartbeatTokenKnown(ctx, second.Token); err != nil || !known {
		t.Errorf("the new token does not match: known=%v err=%v", known, err)
	}
	if again := mustTool[heartbeatTokenOutput](t, s, "get_heartbeat_token", monitorNameInput{Name: "nightly"}); again.Token != second.Token {
		t.Errorf("get_heartbeat_token returned %q, want the rotated %q", again.Token, second.Token)
	}
}

// TestMaintenanceWindowOfATag proves a window can be owned by a tag: it covers
// every monitor carrying the tag, and it is deleted through the tag, which the

// TestTheSetLabelIsNotAValue proves a model that echoes back the label it read
// cannot replace a stored secret with the word "set".
func TestTheSetLabelIsNotAValue(t *testing.T) {
	a := newAPI(t)
	a.addWebhookChannel("ops", "https://hooks.example.com/real-path")
	s := a.session()
	stored, _ := a.storedChannel("ops")

	reason := mustFail(t, s, "update_channel", updateChannelInput{Name: "ops", Channel: channelSpec{
		WebhookURL: ptr("set"), WebhookURLEdit: ptr("replace"),
	}})
	if !strings.Contains(reason, "label a stored value shows") {
		t.Errorf("reason = %q", reason)
	}
	after, _ := a.storedChannel("ops")
	if !sameSealed(after.WebhookURL, stored.WebhookURL) {
		t.Error("the stored webhook URL changed")
	}
}
