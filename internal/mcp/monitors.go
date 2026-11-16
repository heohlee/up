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

// addMonitorTools registers the tools that change monitors, their mutes and
// their maintenance windows. Each one reads the snapshot first, so an update
// starts from what is stored and a tool never resends a value it did not get.
func addMonitorTools(s *sdk.Server, b Backend, now func() time.Time) {
	sdk.AddTool(s, &sdk.Tool{
		Name: "create_monitor",
		Description: "Add a monitor. Only the name and the target are required: an HTTP monitor takes a " +
			"URL, a TCP one a host and port, a ping one a host, a DNS one a domain and a record, and a " +
			"heartbeat a period (or a cron expression) and a grace. Everything else comes from the " +
			"defaults; see get_defaults and set_defaults. A heartbeat gets a ping URL on the server's " +
			"API listener; read it with get_heartbeat_token.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in createMonitorInput) (*sdk.CallToolResult, writeResult, error) {
		snap, err := b.LoadSnapshot(ctx, now())
		if err != nil {
			return nil, writeResult{}, err
		}
		base := newMonitor(in.Name, snap.NewItemDefaults())
		if in.Monitor.Kind != nil && *in.Monitor.Kind == config.CheckHeartbeat {
			// A heartbeat runs nowhere, so it has no interval, timeout or
			// expected status: the defaults supply only the fields every kind
			// carries, as the new monitor form does when its type is switched.
			base.Interval, base.Timeout, base.ExpectedStatus = 0, 0, 0
		}
		check, err := applySpec(base, in.Monitor, lazySealer(ctx, b))
		if err != nil {
			return nil, writeResult{}, err
		}
		if err := config.ValidateCheck(check); err != nil {
			return nil, writeResult{}, err
		}
		if err := b.AddCheck(ctx, check); err != nil {
			return nil, writeResult{}, err
		}
		return nil, writeResult{OK: true, Message: fmt.Sprintf("monitor %q added", in.Name)}, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "update_monitor",
		Description: "Change the fields of a monitor you send and leave the rest as they are. Read it with " +
			"get_monitor first: header values and the request body read as set and stay as they are unless " +
			"you send edit replace with a value. A list you send (headers, conditions, tags) replaces the " +
			"stored one, so re-send what you want to keep. A monitor cannot be renamed.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in updateMonitorInput) (*sdk.CallToolResult, writeResult, error) {
		svc, err := serviceOf(ctx, b, now, in.Name)
		if err != nil {
			return nil, writeResult{}, err
		}
		check, err := applySpec(svc.Check, in.Monitor, lazySealer(ctx, b))
		if err != nil {
			return nil, writeResult{}, err
		}
		check.Name = svc.Check.Name
		if err := config.ValidateCheck(check); err != nil {
			return nil, writeResult{}, err
		}
		if err := b.UpdateCheck(ctx, check); err != nil {
			return nil, writeResult{}, err
		}
		return nil, writeResult{OK: true, Message: fmt.Sprintf("monitor %q updated", in.Name)}, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "delete_monitor",
		Description: "Delete a monitor with its history, incidents and alerts. This cannot be undone, so it " +
			"only runs with confirm true; ask the user to confirm before setting it.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in deleteMonitorInput) (*sdk.CallToolResult, writeResult, error) {
		if !in.Confirm {
			return nil, writeResult{}, fmt.Errorf("delete %q needs confirm true: it removes the monitor, its history and its alerts, and cannot be undone", in.Name)
		}
		if err := b.DeleteCheck(ctx, in.Name); err != nil {
			return nil, writeResult{}, err
		}
		return nil, writeResult{OK: true, Message: fmt.Sprintf("monitor %q deleted", in.Name)}, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "mute_monitor",
		Description: "Hold a monitor's alerts and certificate warnings for a duration, between 1m and 168h " +
			"(7 days). Checks and history keep running, so downtime still counts. When the mute ends the " +
			"recipients hear only what they do not already know.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in muteInput) (*sdk.CallToolResult, writeResult, error) {
		svc, err := serviceOf(ctx, b, now, in.Name)
		if err != nil {
			return nil, writeResult{}, err
		}
		d, err := muteDuration(in.Duration)
		if err != nil {
			return nil, writeResult{}, err
		}
		until := now().Add(d)
		if err := b.SetMute(ctx, in.Name, until, svc.KnownAlerting); err != nil {
			return nil, writeResult{}, err
		}
		return nil, writeResult{OK: true, Message: fmt.Sprintf("%q muted until %s", in.Name, formatTime(until))}, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "unmute_monitor",
		Description: "End a monitor's own mute now. Alerts resume and recipients hear what they missed: a " +
			"down alert if it is still down, or the recovery of an outage they were told about.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in monitorNameInput) (*sdk.CallToolResult, writeResult, error) {
		if err := b.Unmute(ctx, in.Name, now()); err != nil {
			return nil, writeResult{}, err
		}
		return nil, writeResult{OK: true, Message: fmt.Sprintf("%q unmuted", in.Name)}, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "mute_tag",
		Description: "Hold the alerts of every monitor carrying a tag, including monitors tagged later, for " +
			"between 1m and 168h. A monitor's own mute and a tag's mute both hold it; the later end wins.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in muteTagInput) (*sdk.CallToolResult, writeResult, error) {
		if err := tagArg(in.Tag); err != nil {
			return nil, writeResult{}, err
		}
		tags, ok := b.(tui.TagMuter)
		if !ok {
			return nil, writeResult{}, errors.New("this server cannot mute tags")
		}
		d, err := muteDuration(in.Duration)
		if err != nil {
			return nil, writeResult{}, err
		}
		until := now().Add(d)
		if err := tags.SetTagMute(ctx, in.Tag, until); err != nil {
			return nil, writeResult{}, err
		}
		return nil, writeResult{OK: true, Message: fmt.Sprintf("#%s muted until %s", in.Tag, formatTime(until))}, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name:        "unmute_tag",
		Description: "End a tag's mute for every monitor carrying it. Monitors with their own mute stay muted.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in tagInput) (*sdk.CallToolResult, writeResult, error) {
		if err := tagArg(in.Tag); err != nil {
			return nil, writeResult{}, err
		}
		tags, ok := b.(tui.TagMuter)
		if !ok {
			return nil, writeResult{}, errors.New("this server cannot mute tags")
		}
		if err := tags.UnmuteTag(ctx, in.Tag); err != nil {
			return nil, writeResult{}, err
		}
		return nil, writeResult{OK: true, Message: fmt.Sprintf("#%s unmuted", in.Tag)}, nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "add_maintenance_window",
		Description: "Add a recurring weekly window that holds a monitor's alerts and certificate warnings " +
			"while checks and history keep running. Up to 20 per monitor. Use a tag to add one for every " +
			"monitor carrying it at once.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in addWindowInput) (*sdk.CallToolResult, windowView, error) {
		store, ok := b.(tui.MaintenanceStore)
		if !ok {
			return nil, windowView{}, errNotMaintenanceStore
		}
		if err := windowOwner(in.Name, in.Tag); err != nil {
			return nil, windowView{}, err
		}
		w, err := maintenanceOf(in.Name, in.Tag, in.Window)
		if err != nil {
			return nil, windowView{}, err
		}
		if err := config.ValidateMaintenanceWindow(w); err != nil {
			return nil, windowView{}, err
		}
		added, err := store.AddMaintenanceWindow(ctx, w)
		if err != nil {
			return nil, windowView{}, err
		}
		return nil, windowOf(added, config.Maintenance{}), nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "update_maintenance_window",
		Description: "Replace an existing maintenance window of a monitor or a tag. Read the monitor with " +
			"get_monitor to find the window id; the whole window is replaced.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in updateWindowInput) (*sdk.CallToolResult, windowView, error) {
		store, ok := b.(tui.MaintenanceStore)
		if !ok {
			return nil, windowView{}, errNotMaintenanceStore
		}
		if err := windowOwner(in.Name, in.Tag); err != nil {
			return nil, windowView{}, err
		}
		if err := namedWindow(ctx, b, now, in.Name, in.Tag, in.ID); err != nil {
			return nil, windowView{}, err
		}
		w, err := maintenanceOf(in.Name, in.Tag, in.Window)
		if err != nil {
			return nil, windowView{}, err
		}
		w.ID = in.ID
		if err := config.ValidateMaintenanceWindow(w); err != nil {
			return nil, windowView{}, err
		}
		if err := store.UpdateMaintenanceWindow(ctx, w); err != nil {
			return nil, windowView{}, err
		}
		return nil, windowOf(w, config.Maintenance{}), nil
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "delete_maintenance_window",
		Description: "Delete a maintenance window: the monitor's own when name is set, or a tag's when tag " +
			"is set, which ends the hold for every monitor carrying the tag. The window must belong to the " +
			"owner you name. Checks and history are untouched.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in deleteWindowInput) (*sdk.CallToolResult, writeResult, error) {
		db, ok := b.(tui.MaintenanceStore)
		if !ok {
			return nil, writeResult{}, errNotMaintenanceStore
		}
		if err := windowOwner(in.Name, in.Tag); err != nil {
			return nil, writeResult{}, err
		}
		if err := namedWindow(ctx, b, now, in.Name, in.Tag, in.ID); err != nil {
			return nil, writeResult{}, err
		}
		if in.Tag != "" {
			tags, ok := b.(tagWindowDeleter)
			if !ok {
				return nil, writeResult{}, errors.New("this server cannot delete a tag's window; delete it through a monitor carrying the tag")
			}
			err := tags.DeleteTagMaintenanceWindow(ctx, in.Tag, in.ID)
			if err != nil {
				return nil, writeResult{}, err
			}
		} else if err := db.DeleteMaintenanceWindow(ctx, in.Name, in.ID); err != nil {
			return nil, writeResult{}, err
		}
		return nil, writeResult{OK: true, Message: fmt.Sprintf("maintenance window %d deleted", in.ID)}, nil
	})
}

type createMonitorInput struct {
	Name    string      `json:"name" jsonschema:"the monitor name, unique on the server"`
	Monitor monitorSpec `json:"monitor" jsonschema:"the monitor definition"`
}

type updateMonitorInput struct {
	Name    string      `json:"name" jsonschema:"the monitor to change"`
	Monitor monitorSpec `json:"monitor" jsonschema:"the fields to change; omitted fields keep their value"`
}

type deleteMonitorInput struct {
	Name    string `json:"name" jsonschema:"the monitor to delete"`
	Confirm bool   `json:"confirm" jsonschema:"must be true: the delete cannot be undone"`
}

type muteInput struct {
	Name     string `json:"name" jsonschema:"the monitor to mute"`
	Duration string `json:"duration" jsonschema:"how long, from 1m to 168h, such as 2h or 45m"`
}

type muteTagInput struct {
	Tag      string `json:"tag" jsonschema:"the tag whose monitors to mute"`
	Duration string `json:"duration" jsonschema:"how long, from 1m to 168h, such as 2h"`
}

type tagInput struct {
	Tag string `json:"tag" jsonschema:"the tag to unmute"`
}

type addWindowInput struct {
	Name   string          `json:"name,omitempty" jsonschema:"the monitor that gets the window; empty when tag is set"`
	Tag    string          `json:"tag,omitempty" jsonschema:"add the window for every monitor carrying this tag instead"`
	Window maintenanceSpec `json:"window" jsonschema:"the recurring weekly window"`
}

type updateWindowInput struct {
	ID     int64           `json:"id" jsonschema:"the window id from get_monitor"`
	Name   string          `json:"name,omitempty" jsonschema:"the monitor that owns the window; empty when tag is set"`
	Tag    string          `json:"tag,omitempty" jsonschema:"the tag that owns the window instead"`
	Window maintenanceSpec `json:"window" jsonschema:"the window's new definition"`
}

type deleteWindowInput struct {
	ID   int64  `json:"id" jsonschema:"the window id from get_monitor"`
	Name string `json:"name,omitempty" jsonschema:"the monitor that owns the window; empty when tag is set"`
	Tag  string `json:"tag,omitempty" jsonschema:"the tag that owns the window instead"`
}

// serviceOf returns the monitor with this name from a fresh snapshot, so a
// write starts from what the server stores now.
func serviceOf(ctx context.Context, b Backend, now func() time.Time, name string) (tui.Service, error) {
	snap, err := b.LoadSnapshot(ctx, now())
	if err != nil {
		return tui.Service{}, err
	}
	svc, ok := findService(snap, name)
	if !ok {
		return tui.Service{}, fmt.Errorf("no monitor named %q", name)
	}
	return svc, nil
}

// namedWindow checks that window id belongs to the owner the caller named: a
// monitor's own window on one side, a tag's window on the other. It is not
// politeness: the store deletes and updates a tag's window through any monitor
// that carries the tag, so a caller naming only a monitor could end the hold of
// monitors it never mentioned.
func namedWindow(ctx context.Context, b Backend, now func() time.Time, name, tag string, id int64) error {
	if tag != "" {
		snap, err := b.LoadSnapshot(ctx, now())
		if err != nil {
			return err
		}
		for _, svc := range snap.Services {
			if !svc.Check.HasTag(tag) {
				continue
			}
			for _, w := range svc.Maintenance {
				if w.ID != id {
					continue
				}
				if w.Tag != tag {
					return fmt.Errorf("maintenance window %d belongs to %q, not to #%s", id, w.Check, tag)
				}
				return nil
			}
		}
		return fmt.Errorf("no maintenance window %d belongs to #%s", id, tag)
	}
	svc, err := serviceOf(ctx, b, now, name)
	if err != nil {
		return err
	}
	for _, w := range svc.Maintenance {
		if w.ID != id {
			continue
		}
		if w.Tag != "" {
			return fmt.Errorf("maintenance window %d belongs to #%s and holds every monitor carrying it; send tag %q instead of a monitor name", id, w.Tag, w.Tag)
		}
		return nil
	}
	return fmt.Errorf("no maintenance window %d in monitor %q", id, name)
}

// newMonitor is the check a create starts from: the server's defaults for new
// items, which is what the new monitor form of the terminal UI shows, with the
// name already set so validation messages can carry it.
func newMonitor(name string, d config.Defaults) config.Check {
	return config.Check{
		Name:           name,
		Interval:       d.Interval,
		Timeout:        d.Timeout,
		ExpectedStatus: d.ExpectedStatus,
		Channel:        d.Channel,
		AlertAfter:     d.AlertAfter,
		RecoverAfter:   d.RecoverAfter,
	}
}

// muteDuration reads a mute duration and checks its bounds, with the message
// the terminal UI shows.
func muteDuration(raw string) (time.Duration, error) {
	d, err := parseDuration("duration", raw)
	if err != nil {
		return 0, err
	}
	if d < tui.MinMute || d > tui.MaxMute {
		return 0, fmt.Errorf("duration must be between 1m and 168h (7 days)")
	}
	return d, nil
}
