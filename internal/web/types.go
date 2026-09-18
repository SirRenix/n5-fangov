package web

// Contract types of the store-backed endpoints (DESIGN.md "Web and API"):
// the shared vocabulary between internal/web (which serves them) and cmd
// (which implements the stores) — sessions, account, alerts, dashboard,
// about, preset detail/delete/rename/save.

import (
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
)

// Session is one signed-in browser session (cookie). ID is the first 8 hex
// characters of sha256(token); the token itself never leaves the store.
type Session struct {
	ID       string    `json:"id"`
	User     string    `json:"user"`
	IP       string    `json:"ip"`
	Created  time.Time `json:"created"`
	Expires  time.Time `json:"expires"`
	LastSeen time.Time `json:"last_seen"`
	Remember bool      `json:"remember"`
}

// SessionStore keeps the cookie sessions (memory, mirrored to a JSON file
// when one is configured). Implemented by NewSessionStore.
type SessionStore interface {
	Create(user string, remember bool, ip string) (token string, s Session, err error)
	Lookup(token string) (Session, bool)
	Revoke(token string)
	RevokeAll(keepToken string)
	// RevokeAllRename is RevokeAll with the kept session's User set to
	// newUser ("" keeps it) — the rename handler's variant.
	RevokeAllRename(keepToken, newUser string)
	List() []Session
	// SetEpoch binds the store (and its mirror file) to a credential epoch
	// (CredentialEpoch); a mirror written under another epoch is not
	// loaded at start.
	SetEpoch(epoch string)
}

// AccountStore is the daemon's credential file access (implemented in cmd):
// the credentials in effect and their update. Update rewrites [web] user
// and/or password_hash in the config file (comments kept) and returns the
// credentials now in effect; "" keeps the current value.
type AccountStore interface {
	Current() AuthConfig
	Update(user, passwordHash string) (AuthConfig, error)
}

// AlertRecord is one alert that went through the ring (last 50). Error is
// the delivery failure text when the transport reported one; absent for a
// delivered alert.
type AlertRecord struct {
	TS    int64  `json:"ts"`
	Kind  string `json:"kind"`
	Msg   string `json:"msg"`
	Error string `json:"error,omitempty"`
}

// AlertKind describes one alert type for the panel.
type AlertKind struct {
	Kind        string `json:"kind"`
	Description string `json:"description"`
}

// TemplateStatus describes the PVE notification template files.
type TemplateStatus struct {
	Installed bool   `json:"installed"`
	Current   bool   `json:"current"`
	Writable  bool   `json:"writable"`
	Path      string `json:"path"`
	Reason    string `json:"reason,omitempty"`
}

// AlertStatus is the alert transport state for GET /api/alerts. WebhookURL
// is the full URL (the endpoint is protected); logs and the CLI show it
// redacted (alert.RedactURL).
type AlertStatus struct {
	Transport     string         `json:"transport"` // configured: auto | pve | mail | webhook | log | off
	Effective     string         `json:"effective"` // pve-notify | mail | webhook | log | off
	MailTo        string         `json:"mail_to"`
	WebhookURL    string         `json:"webhook_url"`
	WebhookFormat string         `json:"webhook_format"` // json | text
	PVEAvailable  bool           `json:"pve_available"`
	MailAvailable bool           `json:"mail_available"`
	Template      TemplateStatus `json:"template"`
	Cooldown      string         `json:"cooldown"`   // Go duration text ("30m0s"), kept for older clients
	CooldownS     int64          `json:"cooldown_s"` // the same in seconds (0 offline) for the dashboard to format
	Kinds         []AlertKind    `json:"kinds"`
}

// AlertSettings is the [alert] section as PUT /api/alerts sets it. A nil
// member keeps the value in effect (the JSON key was omitted).
type AlertSettings struct {
	Transport     *string
	MailTo        *string
	WebhookURL    *string
	WebhookFormat *string
}

// AlertMgr backs the alerts panel (implemented in cmd). nil → 501.
type AlertMgr interface {
	Status() AlertStatus
	// Recent returns the newest n delivered alerts, newest first.
	Recent(n int) []AlertRecord
	// Test sends an alert of kind "test" now (no cooldown); err is the
	// delivery failure, transport the sink that was tried.
	Test() (transport string, err error)
	// InstallTemplate writes the embedded PVE template files;
	// errors.ErrUnsupported without PVE.
	InstallTemplate() (path string, err error)
	// Configure validates, writes [alert] to the config and applies it.
	// A validation failure is a plain error (400); an I/O failure wraps
	// ErrStore or an OS error (500).
	Configure(s AlertSettings) (AlertStatus, error)
}

// DashboardStore is the watched extra-sensor list (implemented in cmd).
type DashboardStore interface {
	Sensors() []string
	// SetSensors validates (0..8 ids, each parsable), writes [dashboard]
	// sensors and applies it to the controller. Unresolvable ids are kept
	// and reported as warnings.
	SetSensors(ids []string) (warnings []string, err error)
}

// About is the public project card (GET /api/about). No host data.
type About struct {
	Name       string   `json:"name"`
	Version    string   `json:"version"`
	Prerelease string   `json:"prerelease"`
	License    string   `json:"license"`
	LicenseURL string   `json:"license_url"`
	Repo       string   `json:"repo"`
	Author     string   `json:"author"`
	AuthorURL  string   `json:"author_url"`
	Go         string   `json:"go"`
	Credits    []Credit `json:"credits"`
}

// Credit is one upstream acknowledgement.
type Credit struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Note string `json:"note"`
}

// PresetDeleter is optionally implemented by a PresetStore: DELETE
// /api/presets/{name}. ErrPresetBuiltin → 409, fs.ErrNotExist → 404.
type PresetDeleter interface {
	Delete(name string) error
}

// PresetChannel is one [[channel]] table of a preset as GET /api/presets/{name} shows it.
type PresetChannel struct {
	Name       string   `json:"name"`
	PWM        int      `json:"pwm"`
	Sensor     string   `json:"sensor"`
	Curve      [][2]int `json:"curve"`
	Critical   int      `json:"critical"`
	Stop       string   `json:"stop"`
	Hysteresis int      `json:"hysteresis"`
	MinOn      string   `json:"min_on"` // duration string ("0s" = off)
}

// PresetDetail is the full content of one preset.
type PresetDetail struct {
	Name        string          `json:"name"`
	Builtin     bool            `json:"builtin"`
	Description string          `json:"description,omitempty"`
	Channels    []PresetChannel `json:"channels"`
}

// PresetDetailer is optionally implemented by a PresetStore: GET
// /api/presets/{name}. fs.ErrNotExist → 404.
type PresetDetailer interface {
	Detail(name string) (PresetDetail, error)
}

// PresetRenamer is optionally implemented by a PresetStore: POST
// /api/presets/{name}/rename. ErrPresetBuiltin (either name) → 409,
// fs.ErrNotExist → 404, fs.ErrExist (target taken) → 409.
type PresetRenamer interface {
	Rename(oldName, newName string) error
}

// PresetChannelSaver is optionally implemented by a PresetStore: PUT
// /api/presets/{name} with a JSON body stores the composed channels — the
// web layer has validated them with the config's channel rules and checked
// the channel set against the running config. ErrPresetBuiltin → 409.
type PresetChannelSaver interface {
	SaveChannels(name string, chans []config.Channel) error
}
