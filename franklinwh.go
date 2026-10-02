// Package franklinwh is an unofficial client for the FranklinWH cloud API,
// the service behind the FranklinWH mobile app (com.Franklinwh.FamilyEnergy)
// that monitors aGate / aPower home energy systems.
//
// The protocol was reverse engineered from version 2.23.0 of the Android
// app; docs/API.md in this repository describes it in detail. FranklinWH
// does not document or support this API and may change it at any time.
//
// Typical use:
//
//	c := franklinwh.NewClient()
//	if _, err := c.Login(ctx, email, password, nil); err != nil {
//		// errors.Is(err, franklinwh.ErrMFARequired) means a second factor is
//		// needed; finish with c.VerifyMFA.
//	}
//	gateways, _ := c.Gateways(ctx)
//	st, _ := c.Status(ctx, gateways[0].ID)
//	fmt.Println(st.Battery.SoC, st.Solar.PowerKW, st.Grid.PowerKW)
package franklinwh

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultBaseURL is the production endpoint used by the app.
	DefaultBaseURL = "https://energy.franklinwh.com"

	// AppVersion is the FranklinWH Android app version whose requests this
	// package reproduces. It is sent in the softwareVersion header.
	AppVersion = "2.23.0"

	// DefaultLanguage is the value of the lang header for English.
	DefaultLanguage = "EN_US"

	// DefaultUserAgent identifies this library to the server.
	DefaultUserAgent = "franklinwh-go (+https://github.com/lanrat/franklinwh)"

	// gatewayPrefix is the path prefix of the main ("hes-gateway") service.
	gatewayPrefix = "/hes-gateway"
)

// Device describes the phone the client presents itself as. The app sends
// these values with every request (optDevice, optDeviceName and
// optSystemVersion headers); the server only appears to record them.
type Device struct {
	Model     string // optDevice
	Name      string // optDeviceName
	OSVersion string // sent as "Android <OSVersion>" in optSystemVersion
}

// DefaultDevice is used when no Device is configured.
var DefaultDevice = Device{Model: "franklinwh-go", Name: "franklinwh-go", OSVersion: "14"}

// Client talks to the FranklinWH cloud API. Create one with NewClient. A
// Client is safe for concurrent use.
type Client struct {
	baseURL    string
	httpClient *http.Client
	clientID   string
	appVersion string
	lang       string
	userAgent  string
	device     Device
	now        func() time.Time

	mu    sync.RWMutex
	token string
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API base URL (scheme and host, no path).
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient sets the HTTP client used for requests.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// WithToken sets a previously obtained login token, so Login can be skipped.
func WithToken(token string) Option {
	return func(c *Client) { c.token = token }
}

// WithClientID sets the client identifier sent as the client-id header and
// as the MFA device fingerprint. Persist it alongside the token so the
// server sees a stable "device"; MFA's "remember this device" depends on it.
func WithClientID(id string) Option {
	return func(c *Client) { c.clientID = id }
}

// WithDevice sets the phone description sent with each request.
func WithDevice(d Device) Option {
	return func(c *Client) { c.device = d }
}

// WithLanguage sets the lang header (for example "EN_US" or "ZH_CN").
func WithLanguage(lang string) Option {
	return func(c *Client) { c.lang = lang }
}

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua }
}

// WithAppVersion overrides the app version reported to the server.
func WithAppVersion(v string) Option {
	return func(c *Client) { c.appVersion = v }
}

// NewClient returns a Client configured with opts. Without WithClientID a
// random client ID is generated.
func NewClient(opts ...Option) *Client {
	c := &Client{
		baseURL:    DefaultBaseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second}, // the app uses 30s timeouts
		appVersion: AppVersion,
		lang:       DefaultLanguage,
		userAgent:  DefaultUserAgent,
		device:     DefaultDevice,
		now:        time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	if c.clientID == "" {
		c.clientID = NewClientID()
	}
	return c
}

// Token returns the current login token, or "" before a successful login.
func (c *Client) Token() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// SetToken replaces the login token.
func (c *Client) SetToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = token
}

// ClientID returns the client identifier sent to the server.
func (c *Client) ClientID() string { return c.clientID }

// NewClientID returns a random identifier in UUID form, suitable for
// WithClientID.
func NewClientID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("franklinwh: reading random bytes: " + err.Error())
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
