// Package mobile is a gomobile-friendly facade over package franklinwh, for
// building the Android app's AAR with
//
//	gomobile bind -target=android ./mobile
//
// gomobile can only export simple types (ints, floats, bool, string, []byte
// and structs or interfaces made of them) and has no context.Context, so this
// package:
//
//   - returns structured data as JSON strings, described by package schema,
//     which the app decodes with kotlinx.serialization;
//   - blocks on every call, with a per-call timeout; call it off the main
//     thread. Cancel aborts calls in flight;
//   - reports errors as "<kind>: <message>", where kind is one of the
//     ErrKind* constants, because gomobile turns an error into a Java
//     exception that keeps only its message. ErrorKind extracts the kind.
package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lanrat/franklinwh"
	"github.com/lanrat/franklinwh/mobile/schema"
)

// Error kinds, the prefix of every error message this package returns.
const (
	// ErrKindUnauthorized means the token is missing, invalid or expired:
	// show the login screen.
	ErrKindUnauthorized = "unauthorized"
	// ErrKindRateLimited means the server rejected the call as too
	// frequent: wait, then poll less often.
	ErrKindRateLimited = "rate_limited"
	// ErrKindNetwork means the server could not be reached or timed out.
	ErrKindNetwork = "network"
	// ErrKindAPI means the server rejected the request.
	ErrKindAPI = "api"
	// ErrKindInvalid means an argument was invalid; nothing was sent.
	ErrKindInvalid = "invalid"
	// ErrKindNoGateways means the account has no gateways.
	ErrKindNoGateways = "no_gateways"
	// ErrKindCanceled means Cancel aborted the call.
	ErrKindCanceled = "canceled"
	// ErrKindOther is any other failure.
	ErrKindOther = "error"
)

// DefaultTimeoutSeconds bounds each call when Config.TimeoutSeconds is 0.
const DefaultTimeoutSeconds = 45

// Config configures NewClient. Every field is optional.
type Config struct {
	// BaseURL overrides the API endpoint (franklinwh.DefaultBaseURL).
	BaseURL string
	// Token is a saved login token, to skip logging in.
	Token string
	// ClientID is the saved client ID. Leave empty on first run, then
	// persist Client.ClientID(): MFA's "remember this device" depends on it
	// staying the same.
	ClientID string
	// DeviceModel, DeviceName and OSVersion describe the phone, e.g.
	// Build.MODEL, the device name and Build.VERSION.RELEASE.
	DeviceModel string
	DeviceName  string
	OSVersion   string
	// Language is the lang header, e.g. "EN_US".
	Language string
	// TimeoutSeconds bounds each call; 0 means DefaultTimeoutSeconds.
	TimeoutSeconds int
}

// Client wraps a franklinwh.Client. It is safe for concurrent use.
type Client struct {
	c       *franklinwh.Client
	timeout time.Duration

	mu     sync.Mutex
	base   context.Context
	cancel context.CancelFunc
}

// NewClient returns a Client configured by cfg, which may be nil.
func NewClient(cfg *Config) *Client {
	if cfg == nil {
		cfg = &Config{}
	}
	var opts []franklinwh.Option
	if cfg.BaseURL != "" {
		opts = append(opts, franklinwh.WithBaseURL(cfg.BaseURL))
	}
	if cfg.Token != "" {
		opts = append(opts, franklinwh.WithToken(cfg.Token))
	}
	if cfg.ClientID != "" {
		opts = append(opts, franklinwh.WithClientID(cfg.ClientID))
	}
	if cfg.DeviceModel != "" || cfg.DeviceName != "" || cfg.OSVersion != "" {
		d := franklinwh.DefaultDevice
		if cfg.DeviceModel != "" {
			d.Model = cfg.DeviceModel
		}
		if cfg.DeviceName != "" {
			d.Name = cfg.DeviceName
		}
		if cfg.OSVersion != "" {
			d.OSVersion = cfg.OSVersion
		}
		opts = append(opts, franklinwh.WithDevice(d))
	}
	if cfg.Language != "" {
		opts = append(opts, franklinwh.WithLanguage(cfg.Language))
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = DefaultTimeoutSeconds * time.Second
	}
	m := &Client{c: franklinwh.NewClient(opts...), timeout: timeout}
	m.base, m.cancel = context.WithCancel(context.Background())
	return m
}

// Token returns the current login token, or "" when logged out. Persist it
// after Login or VerifyMFA succeeds.
func (m *Client) Token() string { return m.c.Token() }

// SetToken replaces the login token.
func (m *Client) SetToken(token string) { m.c.SetToken(token) }

// ClientID returns the client ID sent to the server. Persist it.
func (m *Client) ClientID() string { return m.c.ClientID() }

// Cancel aborts every call in flight; they fail with ErrKindCanceled. Later
// calls are unaffected.
func (m *Client) Cancel() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancel()
	m.base, m.cancel = context.WithCancel(context.Background())
}

func (m *Client) ctx() (context.Context, context.CancelFunc) {
	m.mu.Lock()
	base := m.base
	m.mu.Unlock()
	return context.WithTimeout(base, m.timeout)
}

// Login signs in with an email and password. timezone (an IANA name such as
// "America/Chicago") is optional. It returns schema.LoginResult as JSON; when
// mfaRequired is false the client is logged in and Token is set.
func (m *Client) Login(email, password, timezone string) (string, error) {
	if email == "" || password == "" {
		return "", invalid("email and password are required")
	}
	ctx, cancel := m.ctx()
	defer cancel()
	res, err := m.c.Login(ctx, email, password, &franklinwh.LoginOptions{Timezone: timezone})
	if errors.Is(err, franklinwh.ErrMFARequired) {
		return toJSON(schema.LoginResult{
			MFARequired: true,
			MFAToken:    res.MFAToken,
			Method:      res.PreferredMFA(),
			Methods:     res.AvailableMFA,
			MaskedEmail: res.MaskedEmail,
		})
	}
	if err != nil {
		return "", wrap(err)
	}
	return toJSON(schema.LoginResult{})
}

// SendEmailOTP emails a one-time code for an MFA login in progress.
func (m *Client) SendEmailOTP(mfaToken string) error {
	ctx, cancel := m.ctx()
	defer cancel()
	return wrap(m.c.SendEmailOTP(ctx, mfaToken))
}

// VerifyMFA completes an MFA login with a code for method ("TOTP" or
// "EMAIL_OTP"). Set remember to skip MFA for this client ID next time.
func (m *Client) VerifyMFA(mfaToken, method, code string, remember bool) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return invalid("a code is required")
	}
	ctx, cancel := m.ctx()
	defer cancel()
	_, err := m.c.VerifyMFA(ctx, mfaToken, method, code, remember)
	return wrap(err)
}

// Logout invalidates the token on the server and clears it locally. The
// token is cleared even when the server call fails.
func (m *Client) Logout() error {
	if m.c.Token() == "" {
		return nil
	}
	ctx, cancel := m.ctx()
	defer cancel()
	return wrap(m.c.Logout(ctx))
}

// Gateways returns the account's gateways as a JSON array of schema.Gateway.
func (m *Client) Gateways() (string, error) {
	ctx, cancel := m.ctx()
	defer cancel()
	gws, err := m.c.Gateways(ctx)
	if err != nil {
		return "", wrap(err)
	}
	out := make([]schema.Gateway, len(gws))
	for i, g := range gws {
		out[i] = schema.Gateway{
			ID: g.ID, Name: g.Name, Status: g.Status, ActiveStatus: g.ActiveStatus,
			Version: g.Version, ProtocolVer: g.ProtocolVer, ZoneInfo: g.ZoneInfo, Address: g.Address,
		}
	}
	return toJSON(out)
}

// now is replaced in tests.
var now = time.Now

// Status fetches live data for a gateway ("" means the first one) and
// returns schema.Status as JSON.
func (m *Client) Status(gatewayID string) (string, error) {
	ctx, cancel := m.ctx()
	defer cancel()
	id, err := m.resolveGateway(ctx, gatewayID)
	if err != nil {
		return "", err
	}
	st, err := m.c.Status(ctx, id)
	if err != nil {
		return "", wrap(err)
	}
	rd := st.Raw.RuntimeData
	units := make([]schema.BatteryUnit, max(len(rd.FhpSn), len(rd.FhpSoc), len(rd.FhpPower)))
	for i := range units {
		units[i] = schema.BatteryUnit{Serial: at(rd.FhpSn, i), SoC: at(rd.FhpSoc, i), PowerKW: at(rd.FhpPower, i)}
	}
	return toJSON(schema.Status{
		GatewayID:  st.GatewayID,
		Mode:       st.Mode,
		HomeLoadKW: st.HomeLoadKW,
		Battery: schema.BatteryStatus{
			SoC: st.Battery.SoC, PowerKW: st.Battery.PowerKW, Charging: st.Battery.Charging, Units: units,
		},
		Grid:      schema.GridStatus{PowerKW: st.Grid.PowerKW, Importing: st.Grid.Importing, Connected: st.Grid.Connected},
		Solar:     schema.SolarStatus{PowerKW: st.Solar.PowerKW, Producing: st.Solar.Producing},
		Generator: schema.GeneratorStatus{PowerKW: st.Generator.PowerKW, Running: st.Generator.Running},
		Energy: schema.EnergyTotals{
			GridImportKWh: rd.KwhUtiIn, GridExportKWh: rd.KwhUtiOut, SolarKWh: rd.KwhSun,
			GeneratorKWh: rd.KwhGen, BatteryChargeKWh: rd.KwhFhpChg, BatteryDischargeKWh: rd.KwhFhpDi,
			HomeKWh: rd.KwhLoad,
		},
		AmbientTemp: rd.TAmb,
		UpdatedAt:   now().Format(time.RFC3339),
	})
}

// compositeInfoPath is the endpoint behind franklinwh.Client.Status.
const compositeInfoPath = "/hes-gateway/terminal/getDeviceCompositeInfo"

// RawStatus returns the unmodified getDeviceCompositeInfo result for a
// gateway ("" means the first one), for a debug screen or bug reports.
func (m *Client) RawStatus(gatewayID string) (string, error) {
	ctx, cancel := m.ctx()
	defer cancel()
	id, err := m.resolveGateway(ctx, gatewayID)
	if err != nil {
		return "", err
	}
	resp, err := m.c.Do(ctx, "GET", compositeInfoPath, url.Values{"gatewayId": {id}, "refreshFlag": {"1"}}, nil)
	if err != nil {
		return "", wrap(err)
	}
	return string(resp.Result), nil
}

// GridLimits returns a gateway's ("" means the first one) grid
// import/export limits as schema.GridLimits JSON.
func (m *Client) GridLimits(gatewayID string) (string, error) {
	ctx, cancel := m.ctx()
	defer cancel()
	id, err := m.resolveGateway(ctx, gatewayID)
	if err != nil {
		return "", err
	}
	l, err := m.c.GridLimits(ctx, id)
	if err != nil {
		return "", wrap(err)
	}
	return toJSON(gridLimitsOf(id, l, nil))
}

// UpdateGridLimits sets a gateway's ("" means the first one) grid limits in
// kW and returns the limits read back afterwards as schema.GridLimits JSON.
// Pass NaN (Double.NaN) to leave a limit unchanged; negative values are
// rejected.
func (m *Client) UpdateGridLimits(gatewayID string, importKW, exportKW float64) (string, error) {
	imp, err := limitArg("import", importKW)
	if err != nil {
		return "", err
	}
	exp, err := limitArg("export", exportKW)
	if err != nil {
		return "", err
	}
	if imp == nil && exp == nil {
		return "", invalid("no limit to change")
	}
	ctx, cancel := m.ctx()
	defer cancel()
	id, err := m.resolveGateway(ctx, gatewayID)
	if err != nil {
		return "", err
	}
	after, err := m.c.UpdateGridLimits(ctx, id, imp, exp)
	applied := err == nil
	if err != nil && !errors.Is(err, franklinwh.ErrGridLimitsNotApplied) {
		return "", wrap(err)
	}
	return toJSON(gridLimitsOf(id, after, &applied))
}

func limitArg(name string, kw float64) (*float64, error) {
	switch {
	case math.IsNaN(kw):
		return nil, nil
	case kw < 0 || math.IsInf(kw, 0):
		return nil, invalid(name + " limit must be a non-negative number of kW")
	}
	return &kw, nil
}

func gridLimitsOf(id string, l *franklinwh.GridLimits, applied *bool) schema.GridLimits {
	return schema.GridLimits{
		GatewayID:     id,
		ImportKW:      l.ImportKW,
		ImportLimited: l.ImportFlag == franklinwh.GridLimitLimited,
		ExportKW:      l.ExportKW,
		ExportLimited: l.ExportFlag == franklinwh.GridLimitLimited,
		Applied:       applied,
	}
}

func (m *Client) resolveGateway(ctx context.Context, id string) (string, error) {
	if id != "" {
		return id, nil
	}
	gw, _, err := m.c.DefaultGateway(ctx)
	if err != nil {
		return "", wrap(err)
	}
	return gw.ID, nil
}

// ErrorKind returns the kind (an ErrKind* constant) of an error message
// returned by this package, e.g. from a Java exception's getMessage().
func ErrorKind(message string) string {
	kind, _, ok := strings.Cut(message, ": ")
	if !ok {
		return ErrKindOther
	}
	switch kind {
	case ErrKindUnauthorized, ErrKindRateLimited, ErrKindNetwork, ErrKindAPI, ErrKindInvalid,
		ErrKindNoGateways, ErrKindCanceled, ErrKindOther:
		return kind
	}
	return ErrKindOther
}

// kindError prefixes an error's message with its kind.
type kindError struct {
	kind string
	err  error
}

func (e *kindError) Error() string { return e.kind + ": " + e.err.Error() }
func (e *kindError) Unwrap() error { return e.err }

func invalid(msg string) error { return &kindError{ErrKindInvalid, errors.New(msg)} }

// wrap classifies err; nil stays nil.
func wrap(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *franklinwh.Error
	var netErr net.Error
	kind := ErrKindOther
	switch {
	case errors.Is(err, franklinwh.ErrUnauthorized):
		kind = ErrKindUnauthorized
	case errors.Is(err, franklinwh.ErrRateLimited):
		kind = ErrKindRateLimited
	case errors.Is(err, franklinwh.ErrNoGateways):
		kind = ErrKindNoGateways
	case errors.Is(err, context.Canceled):
		kind = ErrKindCanceled
	case errors.As(err, &apiErr):
		kind = ErrKindAPI
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr):
		kind = ErrKindNetwork
	}
	return &kindError{kind, err}
}

func toJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", &kindError{ErrKindOther, fmt.Errorf("encoding result: %w", err)}
	}
	return string(b), nil
}

func at[T any](s []T, i int) T {
	var zero T
	if i < len(s) {
		return s[i]
	}
	return zero
}
