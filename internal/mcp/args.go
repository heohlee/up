package mcp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
	"github.com/heohlee/up/internal/store"
)

// The argument helpers below turn what a model sends into the values the store
// takes, and fail with the same words the terminal UI uses. A rejected
// argument never reaches the server, so it never changes anything.

// parseDuration reads a Go duration string such as 30s or 1h30m.
func parseDuration(field, raw string) (time.Duration, error) {
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration such as 30s, 5m or 1h30m", field)
	}
	if d < 0 {
		return 0, fmt.Errorf("%s cannot be negative", field)
	}
	return d, nil
}

// parseEdit reads how a write treats a sealed value: keep, replace or clear.
func parseEdit(field, raw string) (config.Edit, error) {
	if raw == "" {
		return config.EditKeep, nil
	}
	edit, err := config.ParseEdit(raw)
	if err != nil {
		return config.EditKeep, fmt.Errorf("%s: %w", field, err)
	}
	return edit, nil
}

// sealer encrypts a plaintext secret to the server's public key, exactly as
// the terminal UI does before it sends a write.
type sealer func(plaintext string) (secret.Sealed, error)

// sealerFor reads the server's public key. A server that never ran with a
// database key reports a clear error instead of accepting a secret it cannot
// seal.
func sealerFor(ctx context.Context, b Backend) (sealer, error) {
	pub, has, err := b.PublicKey(ctx)
	if err != nil {
		// A remote client reads nothing per resource and refuses the direct
		// call: its snapshot is the read path and carries the key too. The
		// snapshot's clock only shifts times, so the key is the same at any
		// instant.
		snap, snapErr := b.LoadSnapshot(ctx, time.Now())
		if snapErr != nil {
			return nil, err
		}
		pub, has = snap.PublicKey, snap.HasKey
	}
	if !has || len(pub) == 0 {
		return nil, errors.New("the server has no encryption key yet; start `up serve` once, then try again")
	}
	return func(plaintext string) (secret.Sealed, error) { return secret.Seal(pub, plaintext) }, nil
}

// lazySealer reads the key on the first replace, so a write that touches no
// secret works on a server that never created one.
func lazySealer(ctx context.Context, b Backend) sealer {
	var (
		once sync.Once
		seal sealer
		err  error
	)
	return func(plaintext string) (secret.Sealed, error) {
		once.Do(func() { seal, err = sealerFor(ctx, b) })
		if err != nil {
			return secret.Sealed{}, err
		}
		return seal(plaintext)
	}
}

// sealValue resolves one sealed argument pair (an edit and an optional value)
// into the value a write carries and the edit it tells the store.
func sealValue(seal sealer, field, plaintext, editText string) (secret.Sealed, config.Edit, error) {
	edit, err := parseEdit(field+"_edit", editText)
	if err != nil {
		return secret.Sealed{}, edit, err
	}
	switch edit {
	case config.EditClear:
		return secret.Sealed{}, config.EditClear, nil
	case config.EditKeep:
		if plaintext != "" {
			return secret.Sealed{}, config.EditKeep, fmt.Errorf("%s is set but %s_edit is not replace", field, field)
		}
		return secret.Sealed{}, config.EditKeep, nil
	}
	if plaintext == "" {
		return secret.Sealed{}, config.EditReplace, fmt.Errorf("%s_edit is replace but %s is empty", field, field)
	}
	if plaintext == "set" {
		// "set" is what a stored value reads as; a model that echoes the label
		// back would replace the secret with it.
		return secret.Sealed{}, config.EditReplace, fmt.Errorf("%s is the label a stored value shows, not a value: send the real one", field)
	}
	if seal == nil {
		return secret.Sealed{}, config.EditReplace, errors.New("this server holds no encryption key")
	}
	sealed, err := seal(plaintext)
	if err != nil {
		return secret.Sealed{}, config.EditReplace, err
	}
	return sealed, config.EditReplace, nil
}

// headerSpec is one request header. The value is sealed, so it is sent once and
// never read back; on an update, keep leaves the stored value of the header
// with the same name alone.
type headerSpec struct {
	Name  string `json:"name" jsonschema:"the header name, unique ignoring case"`
	Value string `json:"value,omitempty" jsonschema:"the header value; it passes through the model"`
	Edit  string `json:"edit,omitempty" jsonschema:"keep, replace or clear; default keep"`
}

// conditionSpec is one extra rule an HTTP response must meet.
type conditionSpec struct {
	Kind    string `json:"kind" jsonschema:"json, regex, response_time or header"`
	Path    string `json:"path,omitempty" jsonschema:"json: dot-separated path such as services.0.status"`
	Value   string `json:"value,omitempty" jsonschema:"json and header: the value it must equal; empty only requires it to exist"`
	Name    string `json:"name,omitempty" jsonschema:"header: the response header name"`
	Pattern string `json:"pattern,omitempty" jsonschema:"regex: a Go regular expression the body must match"`
	Within  string `json:"within,omitempty" jsonschema:"response_time: the limit, such as 800ms; it must be lower than the timeout"`
}

// monitorSpec is a monitor definition. Every field is optional: a create
// starts from the server's defaults, and an update changes only what it sends,
// so a partial update cannot clear a value the caller never mentioned. Send ""
// to clear a string, or an empty list to clear headers, conditions or tags.
type monitorSpec struct {
	Kind           *string         `json:"kind,omitempty" jsonschema:"http (default), tcp, ping, dns or heartbeat"`
	URL            *string         `json:"url,omitempty" jsonschema:"http: the URL to check"`
	Method         *string         `json:"method,omitempty" jsonschema:"http: GET (default), HEAD, POST, PUT, PATCH, DELETE or OPTIONS"`
	Interval       *string         `json:"interval,omitempty" jsonschema:"how often to check, such as 30s"`
	Timeout        *string         `json:"timeout,omitempty" jsonschema:"how long a check may take, such as 5s"`
	ExpectedStatus *int            `json:"expected_status,omitempty" jsonschema:"http: the status the response must have"`
	Headers        []headerSpec    `json:"headers,omitempty" jsonschema:"http: request headers; send [] to remove them all"`
	BodyEdit       *string         `json:"body_edit,omitempty" jsonschema:"http: keep, replace or clear the request body"`
	Body           *string         `json:"body,omitempty" jsonschema:"http: the request body when body_edit is replace"`
	ExpectText     *string         `json:"expect_text,omitempty" jsonschema:"http: literal text the response body must contain"`
	Conditions     []conditionSpec `json:"conditions,omitempty" jsonschema:"http: extra rules; send [] to remove them all"`
	Host           *string         `json:"host,omitempty" jsonschema:"tcp and ping: the host name or IP, IPv6 without brackets"`
	Port           *int            `json:"port,omitempty" jsonschema:"tcp: the port"`
	Domain         *string         `json:"domain,omitempty" jsonschema:"dns: the name to query"`
	Record         *string         `json:"record,omitempty" jsonschema:"dns: A, AAAA, CNAME, MX or TXT"`
	Resolver       *string         `json:"resolver,omitempty" jsonschema:"dns: the resolver, host or host:port; empty uses the system resolver"`
	Expected       *string         `json:"expected,omitempty" jsonschema:"dns: the answer to match; empty accepts any answer"`
	Schedule       *string         `json:"schedule,omitempty" jsonschema:"heartbeat: period (default) or cron"`
	Period         *string         `json:"period,omitempty" jsonschema:"heartbeat: how often the job runs, such as 24h"`
	Cron           *string         `json:"cron,omitempty" jsonschema:"heartbeat: a 5-field cron expression instead of a period"`
	Grace          *string         `json:"grace,omitempty" jsonschema:"heartbeat: how late a ping may be, such as 30m"`
	Channel        *string         `json:"channel,omitempty" jsonschema:"the channel that receives this monitor's alerts; empty means none"`
	AlertAfter     *int            `json:"alert_after,omitempty" jsonschema:"consecutive failures before the down alert"`
	RecoverAfter   *int            `json:"recover_after,omitempty" jsonschema:"consecutive successes before the recovery alert"`
	TLSOff         *bool           `json:"tls_expiry_off,omitempty" jsonschema:"https: turn off certificate expiry warnings"`
	Tags           []string        `json:"tags,omitempty" jsonschema:"at most 5 tags of lowercase letters, digits and -; send [] to remove them all"`
}

// applySpec returns base with the fields spec sets, sealing every secret on
// the way. base is the zero check when creating and the stored check when
// updating.
func applySpec(base config.Check, spec monitorSpec, seal sealer) (config.Check, error) {
	c := base
	setString := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	setString(&c.Kind, spec.Kind)
	setString(&c.URL, spec.URL)
	setString(&c.Method, spec.Method)
	setString(&c.ExpectText, spec.ExpectText)
	setString(&c.Host, spec.Host)
	setString(&c.Domain, spec.Domain)
	setString(&c.Record, spec.Record)
	setString(&c.Resolver, spec.Resolver)
	setString(&c.Expected, spec.Expected)
	setString(&c.Cron, spec.Cron)
	setString(&c.Channel, spec.Channel)
	if spec.Schedule != nil {
		c.Schedule = *spec.Schedule
	}
	if spec.Interval != nil {
		d, err := parseDuration("interval", *spec.Interval)
		if err != nil {
			return c, err
		}
		c.Interval = d
	}
	if spec.Timeout != nil {
		d, err := parseDuration("timeout", *spec.Timeout)
		if err != nil {
			return c, err
		}
		c.Timeout = d
	}
	if spec.Period != nil {
		d, err := parseDuration("period", *spec.Period)
		if err != nil {
			return c, err
		}
		c.Period = d
	}
	if spec.Grace != nil {
		d, err := parseDuration("grace", *spec.Grace)
		if err != nil {
			return c, err
		}
		c.Grace = d
	}
	for name, src := range map[string]*int{
		"expected_status": spec.ExpectedStatus,
		"port":            spec.Port,
		"alert_after":     spec.AlertAfter,
		"recover_after":   spec.RecoverAfter,
	} {
		if src == nil {
			continue
		}
		switch name {
		case "expected_status":
			c.ExpectedStatus = *src
		case "port":
			c.Port = *src
		case "alert_after":
			c.AlertAfter = *src
		case "recover_after":
			c.RecoverAfter = *src
		}
	}
	if spec.TLSOff != nil {
		c.TLSOff = *spec.TLSOff
	}
	if spec.Tags != nil {
		c.Tags = config.NormalizeTags(spec.Tags)
	}
	if spec.Headers != nil {
		headers := make([]config.Header, 0, len(spec.Headers))
		for i, h := range spec.Headers {
			value, edit, err := sealValue(seal, fmt.Sprintf("headers[%d].value", i), h.Value, h.Edit)
			if err != nil {
				return c, err
			}
			headers = append(headers, config.Header{Name: h.Name, Value: value, Edit: edit})
		}
		c.Headers = headers
	}
	if spec.BodyEdit != nil || spec.Body != nil {
		body := ""
		if spec.Body != nil {
			body = *spec.Body
		}
		value, edit, err := sealValue(seal, "body", body, valueOf(spec.BodyEdit))
		if err != nil {
			return c, err
		}
		c.Body, c.BodyEdit = value, edit
	}
	if spec.Conditions != nil {
		conditions := make([]config.Condition, 0, len(spec.Conditions))
		for i, in := range spec.Conditions {
			cond := config.Condition{Kind: in.Kind, Path: in.Path, Value: in.Value, Name: in.Name, Pattern: in.Pattern}
			if in.Within != "" {
				d, err := parseDuration(fmt.Sprintf("conditions[%d].within", i), in.Within)
				if err != nil {
					return c, err
				}
				cond.Within = d
			}
			conditions = append(conditions, cond)
		}
		c.Conditions = conditions
	}
	return c, nil
}

func valueOf(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// channelSpec is a channel definition. Like monitorSpec it changes only what
// it sends, and every secret is sealed on the way out.
type channelSpec struct {
	Kind            *string  `json:"kind,omitempty" jsonschema:"email, webhook, ntfy, telegram, discord, slack or gotify"`
	SMTPServer      *string  `json:"smtp_server,omitempty" jsonschema:"email: the SMTP server name"`
	Recipients      []string `json:"recipients,omitempty" jsonschema:"email: the recipients; [] removes them all"`
	WebhookURL      *string  `json:"webhook_url,omitempty" jsonschema:"webhook, discord and slack: the URL; it passes through the model"`
	WebhookURLEdit  *string  `json:"webhook_url_edit,omitempty" jsonschema:"keep, replace or clear the stored webhook URL"`
	WebhookAuthName *string  `json:"webhook_auth_name,omitempty" jsonschema:"webhook: the auth header name, such as Authorization"`
	WebhookAuth     *string  `json:"webhook_auth,omitempty" jsonschema:"webhook: the auth header value"`
	WebhookAuthEdit *string  `json:"webhook_auth_edit,omitempty" jsonschema:"keep, replace or clear the stored auth value"`
	ServerURL       *string  `json:"server_url,omitempty" jsonschema:"ntfy and gotify: the server URL"`
	Topic           *string  `json:"topic,omitempty" jsonschema:"ntfy: the topic"`
	ChatID          *string  `json:"chat_id,omitempty" jsonschema:"telegram: the chat ID or @channel"`
	Token           *string  `json:"token,omitempty" jsonschema:"telegram and gotify: the bot or app token; ntfy: an optional access token"`
	TokenEdit       *string  `json:"token_edit,omitempty" jsonschema:"keep, replace or clear the stored token"`
	Priority        *string  `json:"priority,omitempty" jsonschema:"ntfy: min, low, default, high or max; gotify: 0 to 10"`
}

// smtpSpec is an SMTP server definition.
type smtpSpec struct {
	Host         *string `json:"host,omitempty" jsonschema:"the relay host"`
	Port         *int    `json:"port,omitempty" jsonschema:"the relay port"`
	TLS          *string `json:"tls,omitempty" jsonschema:"starttls (default), tls or none"`
	Username     *string `json:"username,omitempty" jsonschema:"the login name; empty for a relay without authentication"`
	Password     *string `json:"password,omitempty" jsonschema:"the login password"`
	PasswordEdit *string `json:"password_edit,omitempty" jsonschema:"keep, replace or clear the stored password"`
	Sender       *string `json:"sender,omitempty" jsonschema:"the From address, such as up <alerts@example.com>"`
}

// defaultsSpec is the defaults for new items; every field is optional.
type defaultsSpec struct {
	Zone           *string `json:"zone,omitempty" jsonschema:"the IANA zone of new maintenance windows and heartbeat schedules, such as America/New_York"`
	Channel        *string `json:"channel,omitempty" jsonschema:"the channel new monitors use; empty means none"`
	Interval       *string `json:"interval,omitempty" jsonschema:"the interval of new monitors, such as 30s"`
	Timeout        *string `json:"timeout,omitempty" jsonschema:"the timeout of new monitors, such as 5s"`
	ExpectedStatus *int    `json:"expected_status,omitempty" jsonschema:"the expected status of new HTTP monitors"`
	AlertAfter     *int    `json:"alert_after,omitempty" jsonschema:"consecutive failures before a new monitor alerts"`
	RecoverAfter   *int    `json:"recover_after,omitempty" jsonschema:"consecutive successes before a new monitor recovers"`
}

// writeResult is the answer of a write that changed one thing. Message says
// what happened in the words the terminal UI would show, because a model
// quoting a channel or a monitor back is more useful than a bare ok.
type writeResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// maintenanceSpec is one recurring maintenance window.
type maintenanceSpec struct {
	Weekdays []string `json:"weekdays" jsonschema:"the days it starts: sun, mon, tue, wed, thu, fri, sat"`
	Start    string   `json:"start" jsonschema:"the local start time as HH:MM"`
	Duration string   `json:"duration" jsonschema:"how long it lasts, from 1m to 24h"`
	Zone     string   `json:"zone" jsonschema:"the IANA zone, such as America/New_York"`
}

var errNotMaintenanceStore = errors.New("this server cannot read maintenance windows")

// tagWindowDeleter removes a window that belongs to a tag. The TUI's
// MaintenanceStore only deletes through a monitor, which also removes a window
// of one of that monitor's tags; the remote client reaches the tag directly.
type tagWindowDeleter interface {
	DeleteTagMaintenanceWindow(ctx context.Context, tag string, id int64) error
}

// windowOwner requires exactly one owner: a monitor or a tag. A window with
// both, or with neither, would be stored somewhere the caller did not name.
func windowOwner(name, tag string) error {
	switch {
	case name != "" && tag != "":
		return errors.New("name and tag cannot both be set: a window belongs to a monitor or to a tag")
	case name == "" && tag == "":
		return errors.New("name or tag is required: say which monitor or tag owns the window")
	}
	return nil
}

// maintenanceOf validates a window and returns it for a monitor or a tag.
// check names the monitor and tag the tag; the store ignores whichever does
// not apply.
func maintenanceOf(check, tag string, spec maintenanceSpec) (config.MaintenanceWindow, error) {
	w, err := configWindow(spec)
	if err != nil {
		return w, err
	}
	w.Check, w.Tag = check, tag
	return w, nil
}

// configWindow turns the argument form of a window into the stored one.
func configWindow(spec maintenanceSpec) (config.MaintenanceWindow, error) {
	var w config.MaintenanceWindow
	for _, day := range spec.Weekdays {
		wd, ok := parseWeekday(day)
		if !ok {
			return w, fmt.Errorf("weekdays: %q is not a day such as mon or tuesday", day)
		}
		w.Weekdays = append(w.Weekdays, wd)
	}
	minute, err := parseClock(spec.Start)
	if err != nil {
		return w, err
	}
	d, err := parseDuration("duration", spec.Duration)
	if err != nil {
		return w, err
	}
	w.StartMinute, w.Duration, w.Zone = minute, d, spec.Zone
	return w, nil
}

func parseWeekday(raw string) (time.Weekday, bool) {
	switch len(raw) {
	case 3:
		for i, d := range []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"} {
			if raw == d {
				return time.Weekday(i), true
			}
		}
	case 0:
		return 0, false
	default:
		for i, d := range []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"} {
			if raw == d {
				return time.Weekday(i), true
			}
		}
	}
	return 0, false
}

// parseClock reads HH:MM as minutes after local midnight.
func parseClock(raw string) (int, error) {
	var h, m int
	if _, err := fmt.Sscanf(raw, "%d:%d", &h, &m); err != nil {
		return 0, errors.New("start must be a local time such as 02:30")
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, errors.New("start must be a local time between 00:00 and 23:59")
	}
	return h*60 + m, nil
}

// tagArg validates a tag the way the store does, so an invalid tag fails with
// the same message instead of reaching the server.
func tagArg(tag string) error {
	if err := config.ValidateTag(tag); err != nil {
		return fmt.Errorf("tag: %w", err)
	}
	return nil
}

// attemptsOfChannel indexes attempts by channel for the channel list.
func attemptsByChannel(last []store.Attempt) map[string]store.Attempt {
	byChannel := make(map[string]store.Attempt, len(last))
	for _, a := range last {
		byChannel[a.Channel] = a
	}
	return byChannel
}
