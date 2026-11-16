package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/tui"
)

// maxDeliveries bounds what list_deliveries returns; the snapshot carries the
// latest tui.AttemptLimit attempts of the whole server.
const maxDeliveries = tui.AttemptLimit

// addReadTools registers the tools that change nothing.
func addReadTools(s *sdk.Server, b Backend, now func() time.Time) {
	sdk.AddTool(s, &sdk.Tool{
		Name: "list_monitors",
		Description: "List the monitors of the server with their status, target, tags and whether alerts " +
			"are held. Start here: it is one request and enough to choose what to inspect.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in listMonitorsInput) (*sdk.CallToolResult, listMonitorsOutput, error) {
		snap, err := b.LoadSnapshot(ctx, now())
		if err != nil {
			return nil, listMonitorsOutput{}, err
		}
		out := listMonitorsOutput{}
		for _, svc := range snap.Services {
			row := monitorRow(svc, now())
			if !matchesTag(row, in.Tag) || !matchesState(row, in.State) {
				continue
			}
			out.Monitors = append(out.Monitors, row)
		}
		out.Count = len(out.Monitors)
		return nil, out, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "get_monitor",
		Description: "Show one monitor in full: its configuration (sealed values read as set), recent " +
			"results, incidents of the last 30 days with what became of their alerts, its maintenance " +
			"windows, its certificate and, for a heartbeat, when the next ping is due.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in monitorNameInput) (*sdk.CallToolResult, getMonitorOutput, error) {
		snap, err := b.LoadSnapshot(ctx, now())
		if err != nil {
			return nil, getMonitorOutput{}, err
		}
		svc, ok := findService(snap, in.Name)
		if !ok {
			return nil, getMonitorOutput{}, fmt.Errorf("no monitor named %q", in.Name)
		}
		return nil, getMonitorOutput{Monitor: monitorDetailOf(svc, now(), snap.API.Enabled())}, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "list_deliveries",
		Description: "List the latest alert deliveries, newest first: what was sent, failed or held for a " +
			"mute or a maintenance window. Use it to answer why an alert did or did not arrive. " +
			"The snapshot carries only the latest few hundred attempts.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in listDeliveriesInput) (*sdk.CallToolResult, listDeliveriesOutput, error) {
		snap, err := b.LoadSnapshot(ctx, now())
		if err != nil {
			return nil, listDeliveriesOutput{}, err
		}
		limit := in.Limit
		if limit <= 0 || limit > maxDeliveries {
			limit = 50
		}
		out := listDeliveriesOutput{}
		for _, a := range snap.Attempts {
			if in.Monitor != "" && a.Monitor != in.Monitor {
				continue
			}
			if in.Channel != "" && a.Channel != in.Channel {
				continue
			}
			if len(out.Deliveries) == limit {
				out.Truncated = true
				break
			}
			out.Deliveries = append(out.Deliveries, attemptOf(a))
		}
		out.Count = len(out.Deliveries)
		return nil, out, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "get_defaults",
		Description: "Show the defaults new monitors and maintenance windows start from: zone, channel, " +
			"interval, timeout, expected status and alert thresholds.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, getDefaultsOutput, error) {
		snap, err := b.LoadSnapshot(ctx, now())
		if err != nil {
			return nil, getDefaultsOutput{}, err
		}
		return nil, getDefaultsOutput{Defaults: defaultsOf(snap.NewItemDefaults())}, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "daemon_status",
		Description: "Report whether 'up serve' is running, the server's uptime and incident count over the " +
			"last 30 days, its API listener, and whether it can send pings. A stale last beat means the " +
			"daemon stopped: checks are not running and statuses are the last stored ones.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, daemonStatusOutput, error) {
		snap, err := b.LoadSnapshot(ctx, now())
		if err != nil {
			return nil, daemonStatusOutput{}, err
		}
		at := now()
		out := daemonStatusOutput{
			Monitors:         len(snap.Services),
			Channels:         len(snap.Channels),
			SMTPServers:      len(snap.SMTPServers),
			Incidents30d:     snap.Incidents,
			APIListening:     snap.API.Enabled(),
			APITLS:           snap.API.TLS,
			CanSendPings:     snap.Pings != config.PingDenied,
			PingsUnknown:     snap.Pings == config.PingUnknown,
			HasEncryptionKey: snap.HasKey,
		}
		if snap.HasUptime {
			out.UptimePercent = float64(int(snap.Uptime*100+0.5)) / 100
		}
		if snap.HasBeat {
			out.LastBeat = formatTime(snap.LastBeat)
			out.SecondsSinceBeat = int(at.Sub(snap.LastBeat).Seconds())
			out.BeatStale = at.Sub(snap.LastBeat) > time.Minute
		}
		return nil, out, nil
	})
}

type noArgs struct{}

type listMonitorsInput struct {
	Tag   string `json:"tag,omitempty" jsonschema:"only monitors carrying this tag"`
	State string `json:"state,omitempty" jsonschema:"only monitors whose status starts with this word, such as up, down, pending or failing"`
}

type listMonitorsOutput struct {
	Count    int           `json:"count"`
	Monitors []monitorView `json:"monitors"`
}

type monitorNameInput struct {
	Name string `json:"name" jsonschema:"the monitor name"`
}

type getMonitorOutput struct {
	Monitor monitorDetail `json:"monitor"`
}

type listDeliveriesInput struct {
	Monitor string `json:"monitor,omitempty" jsonschema:"only this monitor's deliveries"`
	Channel string `json:"channel,omitempty" jsonschema:"only this channel's deliveries"`
	Limit   int    `json:"limit,omitempty" jsonschema:"how many to return, default 50, at most 200"`
}

type listDeliveriesOutput struct {
	Count      int           `json:"count"`
	Truncated  bool          `json:"truncated,omitempty"`
	Deliveries []attemptView `json:"deliveries"`
}

type getDefaultsOutput struct {
	Defaults defaultsView `json:"defaults"`
}

type daemonStatusOutput struct {
	LastBeat         string  `json:"last_beat,omitempty"`
	SecondsSinceBeat int     `json:"seconds_since_beat,omitempty"`
	BeatStale        bool    `json:"beat_stale,omitempty"`
	Monitors         int     `json:"monitors"`
	Channels         int     `json:"channels"`
	SMTPServers      int     `json:"smtp_servers"`
	UptimePercent    float64 `json:"uptime_percent_30d,omitempty"`
	Incidents30d     int     `json:"incidents_30d"`
	APIListening     bool    `json:"api_listening"`
	APITLS           bool    `json:"api_tls,omitempty"`
	CanSendPings     bool    `json:"can_send_pings"`
	PingsUnknown     bool    `json:"pings_unknown,omitempty"`
	HasEncryptionKey bool    `json:"has_encryption_key"`
}

// findService returns the monitor with this exact name.
func findService(snap tui.Snapshot, name string) (tui.Service, bool) {
	for _, svc := range snap.Services {
		if svc.Check.Name == name {
			return svc, true
		}
	}
	return tui.Service{}, false
}

// matchesTag reports whether a row carries the tag; an empty tag matches all.
func matchesTag(row monitorView, tag string) bool {
	if tag == "" {
		return true
	}
	for _, t := range row.Tags {
		if t == strings.ToLower(tag) {
			return true
		}
	}
	return false
}

// matchesState reports whether the row's status starts with the word, so down
// matches DOWN but not nothing else; an empty state matches all.
func matchesState(row monitorView, state string) bool {
	if state == "" {
		return true
	}
	return strings.HasPrefix(strings.ToLower(row.Status), strings.ToLower(state))
}

// sortedChannels returns the channels ordered by name, the order the server
// stores them in, so two calls read the same.
func sortedChannels(channels []config.Channel) []config.Channel {
	out := append([]config.Channel(nil), channels...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
