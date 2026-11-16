package mcp

import (
	"sort"
	"strconv"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
	"github.com/heohlee/up/internal/store"
	"github.com/heohlee/up/internal/tui"
)

// The views below are the JSON the tools return. They exist instead of the
// internal structs for three reasons: sealed values must never leave, the
// words must match what the terminal UI shows, and durations and times must
// read the same in every tool.

// timeLayout is RFC 3339 in UTC. The daemon stores UTC, and a model compares
// two of these far more easily than it reads a Go duration or a local zone.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// formatDuration renders a duration the way a config or a tool argument writes
// it back, so a value read from the server can be sent back unchanged.
func formatDuration(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return d.String()
}

// setLabel is what a sealed value reads as: set, or empty. The ciphertext and
// the plaintext both stay on the server.
func setLabel(s secret.Sealed) string {
	if s.Empty() {
		return ""
	}
	return "set"
}

// monitorView is one monitor in a list: enough to choose what to inspect next.
type monitorView struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Target    string   `json:"target"`
	Status    string   `json:"status"`
	Reason    string   `json:"reason,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Channel   string   `json:"channel,omitempty"`
	LatencyMs int64    `json:"latency_ms,omitempty"`
	LastCheck string   `json:"last_check,omitempty"`
	DownSince string   `json:"down_since,omitempty"`
	// Held is why alerts are currently suppressed: muted, maintenance, both,
	// or empty when they are not. HeldUntil is the later end.
	Held      string `json:"held,omitempty"`
	HeldUntil string `json:"held_until,omitempty"`
	// CertDaysLeft is the days left on an HTTPS monitor's certificate; nil
	// when the monitor has none or TLS expiry tracking is off.
	CertDaysLeft  *int   `json:"cert_days_left,omitempty"`
	Interval      string `json:"interval,omitempty"`
	AlertAfter    int    `json:"alert_after,omitempty"`
	RecoverAfter  int    `json:"recover_after,omitempty"`
	HeartbeatNext string `json:"heartbeat_next_by,omitempty"`
}

// monitorDetail is one monitor in full: the list row, its configuration with
// sealed values reduced to set or empty, and the history the detail view shows.
type monitorDetail struct {
	monitorView
	Config      checkConfigView `json:"config"`
	Recent      []resultView    `json:"recent"`
	Incidents   []incidentView  `json:"incidents"`
	Attempts    []attemptView   `json:"recent_alerts"`
	Maintenance []windowView    `json:"maintenance_windows,omitempty"`
	Heartbeat   *heartbeatView  `json:"heartbeat,omitempty"`
	Cert        *certView       `json:"certificate,omitempty"`
	Uptime30d   uptimeView      `json:"uptime_30d"`
}

// checkConfigView is a monitor's configuration. Every sealed value reads as
// set or empty, never as the value itself.
type checkConfigView struct {
	Kind           string          `json:"kind"`
	URL            string          `json:"url,omitempty"`
	Method         string          `json:"method,omitempty"`
	Interval       string          `json:"interval,omitempty"`
	Timeout        string          `json:"timeout,omitempty"`
	ExpectedStatus int             `json:"expected_status,omitempty"`
	Headers        []headerView    `json:"headers,omitempty"`
	Body           string          `json:"body,omitempty"`
	ExpectText     string          `json:"expect_text,omitempty"`
	Conditions     []conditionView `json:"conditions,omitempty"`
	Host           string          `json:"host,omitempty"`
	Port           int             `json:"port,omitempty"`
	Domain         string          `json:"domain,omitempty"`
	Record         string          `json:"record,omitempty"`
	Resolver       string          `json:"resolver,omitempty"`
	Expected       string          `json:"expected,omitempty"`
	Schedule       string          `json:"schedule,omitempty"`
	Period         string          `json:"period,omitempty"`
	Cron           string          `json:"cron,omitempty"`
	Grace          string          `json:"grace,omitempty"`
	Channel        string          `json:"channel,omitempty"`
	AlertAfter     int             `json:"alert_after,omitempty"`
	RecoverAfter   int             `json:"recover_after,omitempty"`
	TLSOff         bool            `json:"tls_expiry_off,omitempty"`
	Tags           []string        `json:"tags,omitempty"`
}

type headerView struct {
	Name  string `json:"name"`
	Value string `json:"value"` // set, or empty
}

type conditionView struct {
	Kind    string `json:"kind"`
	Path    string `json:"path,omitempty"`
	Name    string `json:"name,omitempty"`
	Value   string `json:"value,omitempty"`
	Pattern string `json:"pattern,omitempty"`
	Within  string `json:"within,omitempty"`
}

type resultView struct {
	At         string `json:"at"`
	Status     string `json:"status"`
	LatencyMs  int64  `json:"latency_ms,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	Error      string `json:"error,omitempty"`
	Answer     string `json:"answer,omitempty"`
	Answers    int    `json:"answers,omitempty"`
	Run        string `json:"run,omitempty"`
}

type incidentView struct {
	Start    string `json:"start"`
	End      string `json:"end,omitempty"`
	Ongoing  bool   `json:"ongoing,omitempty"`
	Duration string `json:"duration"`
	Reason   string `json:"reason,omitempty"`
	Alert    string `json:"alert,omitempty"`
	Channel  string `json:"channel,omitempty"`
	AlertWhy string `json:"alert_reason,omitempty"`
}

type attemptView struct {
	At      string `json:"at"`
	Monitor string `json:"monitor,omitempty"`
	Channel string `json:"channel,omitempty"`
	Event   string `json:"event"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

type windowView struct {
	ID       int64    `json:"id"`
	Check    string   `json:"monitor,omitempty"`
	Tag      string   `json:"tag,omitempty"`
	Weekdays []string `json:"weekdays"`
	Start    string   `json:"start"`
	Duration string   `json:"duration"`
	Zone     string   `json:"zone"`
	Active   bool     `json:"active,omitempty"`
}

type heartbeatView struct {
	Waiting   bool   `json:"waiting"`
	LastPing  string `json:"last_ping,omitempty"`
	Deadline  string `json:"deadline,omitempty"`
	NextBy    string `json:"next_by,omitempty"`
	PingReady bool   `json:"api_listening"`
}

type certView struct {
	Host     string `json:"host"`
	Issuer   string `json:"issuer"`
	NotAfter string `json:"not_after"`
	DaysLeft int    `json:"days_left"`
}

type uptimeView struct {
	Up        string  `json:"up"`
	Observed  string  `json:"observed"`
	Percent   float64 `json:"percent"`
	Incidents int     `json:"incidents"`
}

type channelView struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	SMTPServer string   `json:"smtp_server,omitempty"`
	Recipients []string `json:"recipients,omitempty"`
	ServerURL  string   `json:"server_url,omitempty"`
	Topic      string   `json:"topic,omitempty"`
	ChatID     string   `json:"chat_id,omitempty"`
	Priority   string   `json:"priority,omitempty"`
	// WebhookOrigin is the scheme and host only; the path stays sealed.
	WebhookOrigin string       `json:"webhook_origin,omitempty"`
	WebhookURL    string       `json:"webhook_url"` // set, or empty
	WebhookAuth   *headerView  `json:"webhook_auth,omitempty"`
	Token         string       `json:"token"` // set, or empty
	LastAttempt   *attemptView `json:"last_alert,omitempty"`
	Test          *testView    `json:"last_test,omitempty"`
}

type smtpView struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	TLS      string `json:"tls"`
	Username string `json:"username,omitempty"`
	Password string `json:"password"` // set, or empty
	Sender   string `json:"sender"`
}

type testView struct {
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	Requested string `json:"requested"`
	Finished  string `json:"finished,omitempty"`
}

type defaultsView struct {
	Zone           string `json:"zone"`
	Channel        string `json:"channel,omitempty"`
	Interval       string `json:"interval"`
	Timeout        string `json:"timeout"`
	ExpectedStatus int    `json:"expected_status"`
	AlertAfter     int    `json:"alert_after"`
	RecoverAfter   int    `json:"recover_after"`
}

// monitorRow builds the list row of a service at one instant.
func monitorRow(s tui.Service, now time.Time) monitorView {
	v := monitorView{
		Name:         s.Check.Name,
		Kind:         kindOf(s.Check),
		Target:       targetOf(s.Check),
		Status:       s.Label(),
		Tags:         s.Check.Tags,
		Channel:      s.Check.Channel,
		Interval:     formatDuration(s.Check.Interval),
		AlertAfter:   s.Check.FailuresToAlert(),
		RecoverAfter: s.Check.SuccessesToRecover(),
	}
	if s.HasResult {
		v.Reason = s.Latest.Error
		v.LatencyMs = s.Latest.Latency.Milliseconds()
		v.LastCheck = formatTime(s.Latest.At)
	}
	if !s.DownSince.IsZero() && s.Latest.Status != check.StatusUp {
		v.DownSince = formatTime(s.DownSince)
	}
	if s.Muted(now) {
		v.Held, v.HeldUntil = "muted", formatTime(s.Mute.Until)
	}
	if s.InMaintenance.Active {
		end := formatTime(s.InMaintenance.End)
		if v.Held == "" {
			v.Held, v.HeldUntil = "maintenance", end
		} else if end > v.HeldUntil {
			v.Held, v.HeldUntil = "muted and maintenance", end
		}
	}
	if s.HasCert {
		days := int(s.Cert.NotAfter.Sub(now).Hours() / 24)
		v.CertDaysLeft = &days
	}
	if s.Check.Kind == config.CheckHeartbeat && !s.Heartbeat.Deadline.IsZero() {
		v.HeartbeatNext = formatTime(s.Heartbeat.Deadline)
	}
	return v
}

// monitorDetailOf builds the full detail of a service at one instant.
func monitorDetailOf(s tui.Service, now time.Time, apiEnabled bool) monitorDetail {
	d := monitorDetail{
		monitorView: monitorRow(s, now),
		Config:      configView(s.Check),
		Recent:      make([]resultView, 0, len(s.Recent)),
		Incidents:   make([]incidentView, 0, len(s.Incidents)),
		Attempts:    attemptsOf(s.Attempts),
		Uptime30d:   uptimeOf(s),
	}
	for _, r := range s.Recent {
		d.Recent = append(d.Recent, resultOf(r))
	}
	for _, i := range s.Incidents {
		d.Incidents = append(d.Incidents, incidentOf(i, now))
	}
	for _, w := range s.Maintenance {
		d.Maintenance = append(d.Maintenance, windowOf(w, s.InMaintenance))
	}
	if s.HasCert {
		days := int(s.Cert.NotAfter.Sub(now).Hours() / 24)
		d.Cert = &certView{Host: s.Cert.Host, Issuer: s.Cert.Issuer, NotAfter: formatTime(s.Cert.NotAfter), DaysLeft: days}
	}
	if s.Check.Kind == config.CheckHeartbeat {
		h := &heartbeatView{Waiting: s.Heartbeat.Waiting, NextBy: formatTime(s.Heartbeat.Deadline), PingReady: apiEnabled}
		if !s.Heartbeat.Waiting {
			h.LastPing = formatTime(s.Heartbeat.LastPing)
			h.Deadline = formatTime(s.Heartbeat.Deadline)
		}
		d.Heartbeat = h
	}
	return d
}

// configView renders a monitor's configuration without any sealed value.
func configView(c config.Check) checkConfigView {
	v := checkConfigView{
		Kind:           kindOf(c),
		URL:            c.URL,
		Method:         c.Method,
		Interval:       formatDuration(c.Interval),
		Timeout:        formatDuration(c.Timeout),
		ExpectedStatus: c.ExpectedStatus,
		Body:           setLabel(c.Body),
		ExpectText:     c.ExpectText,
		Host:           c.Host,
		Port:           c.Port,
		Domain:         c.Domain,
		Record:         c.Record,
		Resolver:       c.Resolver,
		Expected:       c.Expected,
		Schedule:       c.Schedule,
		Period:         formatDuration(c.Period),
		Cron:           c.Cron,
		Grace:          formatDuration(c.Grace),
		Channel:        c.Channel,
		AlertAfter:     c.AlertAfter,
		RecoverAfter:   c.RecoverAfter,
		TLSOff:         c.TLSOff,
		Tags:           c.Tags,
	}
	for _, h := range c.Headers {
		v.Headers = append(v.Headers, headerView{Name: h.Name, Value: setLabel(h.Value)})
	}
	for _, cond := range c.Conditions {
		v.Conditions = append(v.Conditions, conditionView{
			Kind:    cond.Kind,
			Path:    cond.Path,
			Name:    cond.Name,
			Value:   cond.Value,
			Pattern: cond.Pattern,
			Within:  formatDuration(cond.Within),
		})
	}
	if c.Kind == config.CheckHeartbeat && v.Schedule == "" {
		v.Schedule = config.SchedulePeriod
	}
	return v
}

func resultOf(r check.Result) resultView {
	return resultView{
		At:         formatTime(r.At),
		Status:     string(r.Status),
		LatencyMs:  r.Latency.Milliseconds(),
		StatusCode: r.StatusCode,
		Error:      r.Error,
		Answer:     r.Answer,
		Answers:    r.Answers,
		Run:        formatTime(r.Run),
	}
}

func incidentOf(i tui.Incident, now time.Time) incidentView {
	end := i.End
	if i.Ongoing {
		end = now
	}
	return incidentView{
		Start:    formatTime(i.Start),
		End:      formatTime(i.End),
		Ongoing:  i.Ongoing,
		Duration: formatDuration(end.Sub(i.Start).Round(time.Second)),
		Reason:   i.Error,
		Alert:    string(i.Alert),
		Channel:  i.Channel,
		AlertWhy: i.Reason,
	}
}

func attemptsOf(attempts []store.Attempt) []attemptView {
	views := make([]attemptView, 0, len(attempts))
	for _, a := range attempts {
		views = append(views, attemptView{
			At:      formatTime(a.At),
			Monitor: a.Monitor,
			Channel: a.Channel,
			Event:   string(a.Event),
			Outcome: string(a.Outcome),
			Reason:  a.Reason,
		})
	}
	return views
}

func windowOf(w config.MaintenanceWindow, active config.Maintenance) windowView {
	days := make([]string, 0, len(w.Weekdays))
	for _, d := range w.Weekdays {
		days = append(days, d.String()[:3])
	}
	sort.SliceStable(days, func(i, j int) bool { return weekdayOrder(days[i]) < weekdayOrder(days[j]) })
	return windowView{
		ID:       w.ID,
		Check:    w.Check,
		Tag:      w.Tag,
		Weekdays: days,
		Start:    clockOf(w.StartMinute),
		Duration: formatDuration(w.Duration),
		Zone:     w.Zone,
		Active:   active.Active && !active.End.IsZero(),
	}
}

func weekdayOrder(short string) int {
	for i, d := range []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"} {
		if d == short {
			return i
		}
	}
	return 7
}

// clockOf renders minutes after local midnight as HH:MM.
func clockOf(minute int) string {
	h, m := minute/60, minute%60
	return two(h) + ":" + two(m)
}

func two(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func uptimeOf(s tui.Service) uptimeView {
	v := uptimeView{
		Up:        formatDuration(s.PeriodUp.Round(time.Second)),
		Observed:  formatDuration(s.PeriodObserved.Round(time.Second)),
		Incidents: s.PeriodIncidents,
	}
	if s.PeriodObserved > 0 {
		v.Percent = float64(s.PeriodUp) / float64(s.PeriodObserved) * 100
		v.Percent = float64(int(v.Percent*100+0.5)) / 100
	}
	return v
}

// kindOf is the monitor's type with the HTTP default applied, so every tool
// reports a kind even for a check that stores none.
func kindOf(c config.Check) string {
	if c.Kind == "" {
		return config.CheckHTTP
	}
	return c.Kind
}

// targetOf is the thing the monitor checks: the URL of an HTTP monitor, the
// host and port of a TCP one, the host of a ping one, the name and record of a
// DNS one, or the schedule of a heartbeat.
func targetOf(c config.Check) string {
	switch kindOf(c) {
	case config.CheckTCP:
		return c.Host + ":" + strconv.Itoa(c.Port)
	case config.CheckPing:
		return c.Host
	case config.CheckDNS:
		target := c.Domain + " " + c.Record
		if c.Resolver != "" {
			target += " @" + c.Resolver
		}
		return target
	case config.CheckHeartbeat:
		if c.Schedule == config.ScheduleCron {
			return "cron " + c.Cron + " · grace " + formatDuration(c.Grace)
		}
		return "every " + config.ShortDuration(c.Period) + " · grace " + config.ShortDuration(c.Grace)
	default:
		return c.URL
	}
}

func channelOf(c config.Channel, tests map[string]config.ChannelTest, last map[string]store.Attempt) channelView {
	v := channelView{
		Name:          c.Name,
		Kind:          c.Kind,
		SMTPServer:    c.SMTPServer,
		Recipients:    c.Recipients,
		ServerURL:     c.ServerURL,
		Topic:         c.Topic,
		ChatID:        c.ChatID,
		Priority:      c.Priority,
		WebhookOrigin: c.WebhookOrigin,
		WebhookURL:    setLabel(c.WebhookURL),
		Token:         setLabel(c.Token),
	}
	if c.WebhookAuthName != "" || !c.WebhookAuth.Empty() {
		v.WebhookAuth = &headerView{Name: c.WebhookAuthName, Value: setLabel(c.WebhookAuth)}
	}
	if t, ok := tests[c.Name]; ok {
		v.Test = &testView{Status: string(t.Status), Reason: t.Error, Requested: formatTime(t.Requested), Finished: formatTime(t.Finished)}
	}
	if a, ok := last[c.Name]; ok {
		view := attemptOf(a)
		v.LastAttempt = &view
	}
	return v
}

func attemptOf(a store.Attempt) attemptView {
	return attemptView{
		At:      formatTime(a.At),
		Monitor: a.Monitor,
		Channel: a.Channel,
		Event:   string(a.Event),
		Outcome: string(a.Outcome),
		Reason:  a.Reason,
	}
}

func smtpOf(s config.SMTPServer) smtpView {
	return smtpView{
		Name:     s.Name,
		Host:     s.Host,
		Port:     s.Port,
		TLS:      string(s.TLS),
		Username: s.Username,
		Password: setLabel(s.Password),
		Sender:   s.Sender,
	}
}

func defaultsOf(d config.Defaults) defaultsView {
	return defaultsView{
		Zone:           d.Zone,
		Channel:        d.Channel,
		Interval:       formatDuration(d.Interval),
		Timeout:        formatDuration(d.Timeout),
		ExpectedStatus: d.ExpectedStatus,
		AlertAfter:     d.AlertAfter,
		RecoverAfter:   d.RecoverAfter,
	}
}
