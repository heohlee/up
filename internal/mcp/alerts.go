package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/tui"
)

// addAlertTools registers the tools that manage where alerts go, the defaults
// new items start from, and heartbeat ping tokens.
func addAlertTools(s *sdk.Server, b Backend, now func() time.Time) {
	sdk.AddTool(s, &sdk.Tool{
		Name: "list_channels",
		Description: "List the channels alerts can go to: email through an SMTP server, or a webhook, ntfy, " +
			"telegram, discord, slack or gotify. Sealed values read as set; each channel shows its last " +
			"delivery and its last test.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, listChannelsOutput, error) {
		snap, err := b.LoadSnapshot(ctx, now())
		if err != nil {
			return nil, listChannelsOutput{}, err
		}
		tests := make(map[string]config.ChannelTest, len(snap.ChannelTests))
		for _, t := range snap.ChannelTests {
			tests[t.Channel] = t
		}
		last := attemptsByChannel(snap.LastAttempts)
		out := listChannelsOutput{}
		for _, c := range sortedChannels(snap.Channels) {
			out.Channels = append(out.Channels, channelOf(c, tests, last))
		}
		out.Count = len(out.Channels)
		return nil, out, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "create_channel",
		Description: "Add a channel. Email takes an SMTP server and recipients; webhook a URL and an optional " +
			"auth header; discord and slack a webhook URL; ntfy a server, topic and optional token; telegram " +
			"a bot token and chat id; gotify a server, app token and priority. A secret you send here passes " +
			"through the model: setting it in 'up tui' keeps it out of the transcript.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in createChannelInput) (*sdk.CallToolResult, writeResult, error) {
		channel, err := applyChannel(config.Channel{Name: in.Name}, in.Channel, lazySealer(ctx, b))
		if err != nil {
			return nil, writeResult{}, err
		}
		if err := b.AddChannel(ctx, channel); err != nil {
			return nil, writeResult{}, err
		}
		return nil, writeResult{OK: true, Message: fmt.Sprintf("channel %q added", in.Name)}, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "update_channel",
		Description: "Change the fields of a channel you send and leave the rest as they are. A stored " +
			"webhook URL, auth value or token reads as set and stays unless you send edit replace with a " +
			"value, or edit clear.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in updateChannelInput) (*sdk.CallToolResult, writeResult, error) {
		snap, err := b.LoadSnapshot(ctx, now())
		if err != nil {
			return nil, writeResult{}, err
		}
		current, ok := findChannel(snap, in.Name)
		if !ok {
			return nil, writeResult{}, fmt.Errorf("no channel named %q", in.Name)
		}
		channel, err := applyChannel(current, in.Channel, lazySealer(ctx, b))
		if err != nil {
			return nil, writeResult{}, err
		}
		channel.Name = current.Name
		if err := b.UpdateChannel(ctx, channel); err != nil {
			return nil, writeResult{}, err
		}
		return nil, writeResult{OK: true, Message: fmt.Sprintf("channel %q updated", in.Name)}, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "delete_channel",
		Description: "Delete a channel. A channel a monitor still uses cannot be deleted; set those " +
			"monitors to another channel or none first. The stored secret is gone with it and cannot be " +
			"read back, so this only runs with confirm true.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in deleteChannelInput) (*sdk.CallToolResult, writeResult, error) {
		if !in.Confirm {
			return nil, writeResult{}, fmt.Errorf("delete %q needs confirm true: its stored secret is lost and cannot be read back", in.Name)
		}
		if err := b.DeleteChannel(ctx, in.Name); err != nil {
			return nil, writeResult{}, err
		}
		return nil, writeResult{OK: true, Message: fmt.Sprintf("channel %q deleted", in.Name)}, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "test_channel",
		Description: "Ask the daemon to send a test message through a channel. It answers pending: the " +
			"daemon sends it and the result arrives in the next list_channels as the channel's last test. " +
			"'up serve' must be running.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in channelNameInput) (*sdk.CallToolResult, testView, error) {
		tester, ok := b.(tui.ChannelTester)
		if !ok {
			return nil, testView{}, errors.New("this server cannot test channels")
		}
		test, err := tester.EnqueueChannelTest(ctx, in.Name, now())
		if err != nil {
			return nil, testView{}, err
		}
		return nil, testView{Status: string(test.Status), Reason: test.Error, Requested: formatTime(test.Requested), Finished: formatTime(test.Finished)}, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "list_smtp_servers",
		Description: "List the SMTP servers email channels send through, with their host, port, TLS mode and " +
			"whether a password is stored.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, listSMTPOutput, error) {
		snap, err := b.LoadSnapshot(ctx, now())
		if err != nil {
			return nil, listSMTPOutput{}, err
		}
		out := listSMTPOutput{}
		for _, server := range snap.SMTPServers {
			out.Servers = append(out.Servers, smtpOf(server))
		}
		out.Count = len(out.Servers)
		return nil, out, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "create_smtp_server",
		Description: "Add an SMTP server for email channels. A relay without authentication takes tls none " +
			"and no username. Start 'up serve' once before storing a password: it creates the key the " +
			"password is sealed with.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in createSMTPInput) (*sdk.CallToolResult, writeResult, error) {
		server, err := applySMTP(config.SMTPServer{Name: in.Name}, in.Server, lazySealer(ctx, b))
		if err != nil {
			return nil, writeResult{}, err
		}
		if err := b.AddSMTPServer(ctx, server); err != nil {
			return nil, writeResult{}, err
		}
		return nil, writeResult{OK: true, Message: fmt.Sprintf("SMTP server %q added", in.Name)}, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "update_smtp_server",
		Description: "Change the fields of an SMTP server you send. The stored password reads as set and " +
			"stays unless you send password_edit replace with a value, or password_edit clear.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in updateSMTPInput) (*sdk.CallToolResult, writeResult, error) {
		snap, err := b.LoadSnapshot(ctx, now())
		if err != nil {
			return nil, writeResult{}, err
		}
		current, ok := findSMTP(snap, in.Name)
		if !ok {
			return nil, writeResult{}, fmt.Errorf("no SMTP server named %q", in.Name)
		}
		server, err := applySMTP(current, in.Server, lazySealer(ctx, b))
		if err != nil {
			return nil, writeResult{}, err
		}
		server.Name = current.Name
		if err := b.UpdateSMTPServer(ctx, server); err != nil {
			return nil, writeResult{}, err
		}
		return nil, writeResult{OK: true, Message: fmt.Sprintf("SMTP server %q updated", in.Name)}, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "delete_smtp_server",
		Description: "Delete an SMTP server. One an email channel still uses cannot be deleted. The stored " +
			"password is gone with it, so this only runs with confirm true.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in deleteSMTPInput) (*sdk.CallToolResult, writeResult, error) {
		if !in.Confirm {
			return nil, writeResult{}, fmt.Errorf("delete %q needs confirm true: its stored password is lost", in.Name)
		}
		if err := b.DeleteSMTPServer(ctx, in.Name); err != nil {
			return nil, writeResult{}, err
		}
		return nil, writeResult{OK: true, Message: fmt.Sprintf("SMTP server %q deleted", in.Name)}, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "set_defaults",
		Description: "Change the defaults new monitors and maintenance windows start from: the zone, the " +
			"channel, interval, timeout, expected status and alert thresholds. Existing monitors keep their " +
			"values. Only the fields you send change.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in setDefaultsInput) (*sdk.CallToolResult, getDefaultsOutput, error) {
		store, ok := b.(tui.DefaultsStore)
		if !ok {
			return nil, getDefaultsOutput{}, errors.New("this server cannot save defaults")
		}
		snap, err := b.LoadSnapshot(ctx, now())
		if err != nil {
			return nil, getDefaultsOutput{}, err
		}
		d, err := applyDefaults(snap.NewItemDefaults(), in.Defaults)
		if err != nil {
			return nil, getDefaultsOutput{}, err
		}
		if err := config.ValidateDefaults(d); err != nil {
			return nil, getDefaultsOutput{}, err
		}
		if err := store.SetDefaults(ctx, d); err != nil {
			return nil, getDefaultsOutput{}, err
		}
		return nil, getDefaultsOutput{Defaults: defaultsOf(d)}, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "get_heartbeat_token",
		Description: "Show a heartbeat monitor's ping token and path, to build the cron line or script the " +
			"job needs. The token records pings for that monitor and grants nothing else. It is a " +
			"credential: it lands in this conversation and in the client's logs.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in monitorNameInput) (*sdk.CallToolResult, heartbeatTokenOutput, error) {
		tokens, ok := b.(tui.HeartbeatTokens)
		if !ok {
			return nil, heartbeatTokenOutput{}, errors.New("this server cannot show heartbeat tokens")
		}
		token, err := tokens.RevealHeartbeatToken(ctx, in.Name)
		if err != nil {
			return nil, heartbeatTokenOutput{}, err
		}
		return nil, heartbeatTokenOutput{Token: token, Path: config.HeartbeatPath(token), Warning: heartbeatWarning(in.Name)}, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "rotate_heartbeat_token",
		Description: "Replace a heartbeat's ping token; the old URL stops working at once, so every job " +
			"sending to it must be updated. This only runs with confirm true.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in rotateHeartbeatInput) (*sdk.CallToolResult, heartbeatTokenOutput, error) {
		tokens, ok := b.(tui.HeartbeatTokens)
		if !ok {
			return nil, heartbeatTokenOutput{}, errors.New("this server cannot replace heartbeat tokens")
		}
		if !in.Confirm {
			return nil, heartbeatTokenOutput{}, fmt.Errorf("rotating %q needs confirm true: the old ping URL stops working", in.Name)
		}
		token, err := tokens.RegenerateHeartbeatToken(ctx, in.Name)
		if err != nil {
			return nil, heartbeatTokenOutput{}, err
		}
		return nil, heartbeatTokenOutput{Token: token, Path: config.HeartbeatPath(token), Warning: heartbeatWarning(in.Name)}, nil
	})
}

type createChannelInput struct {
	Name    string      `json:"name" jsonschema:"the channel name, unique on the server"`
	Channel channelSpec `json:"channel" jsonschema:"the channel definition"`
}

type updateChannelInput struct {
	Name    string      `json:"name" jsonschema:"the channel to change"`
	Channel channelSpec `json:"channel" jsonschema:"the fields to change; omitted fields keep their value"`
}

type deleteChannelInput struct {
	Name    string `json:"name" jsonschema:"the channel to delete"`
	Confirm bool   `json:"confirm" jsonschema:"must be true: the stored secret is lost"`
}

type channelNameInput struct {
	Name string `json:"name" jsonschema:"the channel to test"`
}

type listChannelsOutput struct {
	Count    int           `json:"count"`
	Channels []channelView `json:"channels"`
}

type listSMTPOutput struct {
	Count   int        `json:"count"`
	Servers []smtpView `json:"smtp_servers"`
}

type createSMTPInput struct {
	Name   string   `json:"name" jsonschema:"the SMTP server name, unique on the server"`
	Server smtpSpec `json:"server" jsonschema:"the SMTP server definition"`
}

type updateSMTPInput struct {
	Name   string   `json:"name" jsonschema:"the SMTP server to change"`
	Server smtpSpec `json:"server" jsonschema:"the fields to change; omitted fields keep their value"`
}

type deleteSMTPInput struct {
	Name    string `json:"name" jsonschema:"the SMTP server to delete"`
	Confirm bool   `json:"confirm" jsonschema:"must be true: the stored password is lost"`
}

type setDefaultsInput struct {
	Defaults defaultsSpec `json:"defaults" jsonschema:"the defaults to change; omitted fields keep their value"`
}

type rotateHeartbeatInput struct {
	Name    string `json:"name" jsonschema:"the heartbeat monitor whose token to replace"`
	Confirm bool   `json:"confirm" jsonschema:"must be true: the old ping URL stops working"`
}

type heartbeatTokenOutput struct {
	Token   string `json:"token"`
	Path    string `json:"path"`
	Warning string `json:"warning"`
}

// heartbeatWarning is the sentence every tool that reveals a ping token
// returns, so the credential is never quoted without its limits.
func heartbeatWarning(name string) string {
	return fmt.Sprintf("This token only records pings for %q. Keep it out of issue trackers, chats and logs other than the job's own crontab: anyone who has it can make the monitor look up.", name)
}

// applyChannel returns base with the fields spec sets, sealing every secret.
func applyChannel(base config.Channel, spec channelSpec, seal sealer) (config.Channel, error) {
	c := base
	setString := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	setString(&c.Kind, spec.Kind)
	setString(&c.SMTPServer, spec.SMTPServer)
	setString(&c.ServerURL, spec.ServerURL)
	setString(&c.Topic, spec.Topic)
	setString(&c.ChatID, spec.ChatID)
	setString(&c.Priority, spec.Priority)
	if spec.Recipients != nil {
		c.Recipients = spec.Recipients
	}
	url, edit, err := sealValue(seal, "webhook_url", valueOf(spec.WebhookURL), valueOf(spec.WebhookURLEdit))
	if err != nil {
		return c, err
	}
	if edit == config.EditReplace {
		origin, err := config.WebhookOrigin(valueOf(spec.WebhookURL))
		if err != nil {
			return c, err
		}
		c.WebhookOrigin = origin
	}
	c.WebhookURL, c.WebhookURLEdit = url, edit
	auth, authEdit, err := sealValue(seal, "webhook_auth", valueOf(spec.WebhookAuth), valueOf(spec.WebhookAuthEdit))
	if err != nil {
		return c, err
	}
	setString(&c.WebhookAuthName, spec.WebhookAuthName)
	c.WebhookAuth, c.WebhookAuthEdit = auth, authEdit
	token, tokenEdit, err := sealValue(seal, "token", valueOf(spec.Token), valueOf(spec.TokenEdit))
	if err != nil {
		return c, err
	}
	c.Token, c.TokenEdit = token, tokenEdit
	return c, nil
}

// applySMTP returns base with the fields spec sets, sealing the password.
func applySMTP(base config.SMTPServer, spec smtpSpec, seal sealer) (config.SMTPServer, error) {
	s := base
	if spec.Host != nil {
		s.Host = *spec.Host
	}
	if spec.Port != nil {
		s.Port = *spec.Port
	}
	if spec.TLS != nil {
		s.TLS = config.TLSMode(*spec.TLS)
	}
	if spec.Username != nil {
		s.Username = *spec.Username
	}
	if spec.Sender != nil {
		s.Sender = *spec.Sender
	}
	// An SMTP password has no edit marker: the store keeps the stored one
	// when the write carries none, so only replace is representable.
	password, edit, err := sealValue(seal, "password", valueOf(spec.Password), valueOf(spec.PasswordEdit))
	if err != nil {
		return s, err
	}
	if edit == config.EditClear {
		return s, errors.New("an SMTP password cannot be cleared; replace it, or delete the server")
	}
	s.Password = password
	return s, nil
}

// applyDefaults returns base with the fields spec sets.
func applyDefaults(base config.Defaults, spec defaultsSpec) (config.Defaults, error) {
	d := base
	if spec.Zone != nil {
		d.Zone = *spec.Zone
	}
	if spec.Channel != nil {
		d.Channel = *spec.Channel
	}
	for name, src := range map[string]*string{
		"interval": spec.Interval,
		"timeout":  spec.Timeout,
	} {
		if src == nil {
			continue
		}
		value, err := parseDuration(name, *src)
		if err != nil {
			return d, err
		}
		if name == "interval" {
			d.Interval = value
		} else {
			d.Timeout = value
		}
	}
	for name, src := range map[string]*int{
		"expected_status": spec.ExpectedStatus,
		"alert_after":     spec.AlertAfter,
		"recover_after":   spec.RecoverAfter,
	} {
		if src == nil {
			continue
		}
		switch name {
		case "expected_status":
			d.ExpectedStatus = *src
		case "alert_after":
			d.AlertAfter = *src
		case "recover_after":
			d.RecoverAfter = *src
		}
	}
	return d, nil
}

// findChannel returns the channel with this exact name.
func findChannel(snap tui.Snapshot, name string) (config.Channel, bool) {
	for _, c := range snap.Channels {
		if c.Name == name {
			return c, true
		}
	}
	return config.Channel{}, false
}

// findSMTP returns the SMTP server with this exact name.
func findSMTP(snap tui.Snapshot, name string) (config.SMTPServer, bool) {
	for _, s := range snap.SMTPServers {
		if s.Name == name {
			return s, true
		}
	}
	return config.SMTPServer{}, false
}
