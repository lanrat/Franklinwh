// Package gui serves the FranklinWH dashboard: a single web page plus a
// small JSON API that proxies to the FranklinWH cloud. It binds to loopback
// only and every request must carry a per-launch token, so other programs on
// the machine (or apps on the phone) cannot use it. The desktop command and
// the Android app (through package mobile) both run it.
package gui

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lanrat/franklinwh"
)

//go:embed web/index.html
var indexHTML string

// Config configures a Server.
type Config struct {
	Addr        string  // listen address; default 127.0.0.1:0 (random port)
	BaseURL     string  // API base URL override, if any
	SessionPath string  // where to persist the login session ("" disables)
	Session     Session // session loaded at startup
	Email       string  // pre-fill for the login form
	// Device describes the client to the server; nil means the default.
	Device *franklinwh.Device
	// Embedded hides the page's Quit button, for hosts such as the Android
	// app that own the server's lifetime.
	Embedded bool
}

// Server serves the dashboard and proxies to the FranklinWH API.
type Server struct {
	cfg     Config
	client  *franklinwh.Client
	uiToken string // required on every request; part of URL
	index   string // index.html with the token injected

	mu   sync.Mutex // guards session writes
	ping atomic.Int64

	srv          *http.Server
	url          string
	shutdownOnce sync.Once
	done         chan struct{}
}

// New returns a Server for cfg. Call Start to serve.
func New(cfg Config) (*Server, error) {
	tok, err := randomToken()
	if err != nil {
		return nil, err
	}
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:0"
	}
	if cfg.Session.ClientID == "" {
		// A stable client ID lets MFA's "remember this device" take effect.
		cfg.Session.ClientID = franklinwh.NewClientID()
	}
	if cfg.Email == "" {
		cfg.Email = cfg.Session.Email
	}
	opts := []franklinwh.Option{franklinwh.WithClientID(cfg.Session.ClientID)}
	if cfg.BaseURL != "" {
		opts = append(opts, franklinwh.WithBaseURL(cfg.BaseURL))
	}
	if cfg.Session.Token != "" {
		opts = append(opts, franklinwh.WithToken(cfg.Session.Token))
	}
	if cfg.Device != nil {
		opts = append(opts, franklinwh.WithDevice(*cfg.Device))
	}
	embedded := "0"
	if cfg.Embedded {
		embedded = "1"
	}
	index := strings.ReplaceAll(indexHTML, "__UI_TOKEN__", tok)
	index = strings.ReplaceAll(index, "__EMBEDDED__", embedded)
	return &Server{
		cfg:     cfg,
		client:  franklinwh.NewClient(opts...),
		uiToken: tok,
		index:   index,
		done:    make(chan struct{}),
	}, nil
}

// Start listens on the configured address, serves in the background, and
// returns the page's URL, which includes the access token.
func (s *Server) Start() (string, error) {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return "", fmt.Errorf("starting GUI server: %w", err)
	}
	s.url = "http://" + ln.Addr().String() + "/?t=" + s.uiToken
	s.srv = &http.Server{Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "GUI server error:", err)
			s.quit()
		}
	}()
	return s.url, nil
}

// URL returns the page's URL once Start has succeeded.
func (s *Server) URL() string { return s.url }

// Done is closed when the page asks to quit, the heartbeat lapses, or the
// server fails.
func (s *Server) Done() <-chan struct{} { return s.done }

// Close stops the server.
func (s *Server) Close() error {
	s.quit()
	if s.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/session", s.guard(s.handleSession))
	mux.HandleFunc("/api/login", s.guard(s.handleLogin))
	mux.HandleFunc("/api/mfa/send", s.guard(s.handleMFASend))
	mux.HandleFunc("/api/mfa/verify", s.guard(s.handleMFAVerify))
	mux.HandleFunc("/api/logout", s.guard(s.handleLogout))
	mux.HandleFunc("/api/gateways", s.guard(s.handleGateways))
	mux.HandleFunc("/api/status", s.guard(s.handleStatus))
	mux.HandleFunc("/api/grid", s.guard(s.handleGrid))
	mux.HandleFunc("/api/ping", s.guard(s.handlePing))
	mux.HandleFunc("/api/quit", s.guard(s.handleQuit))
	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	// The page embeds the API token, so serving it is guarded too.
	if !isLocalHost(r.Host) || r.URL.Query().Get("t") != s.uiToken {
		http.Error(w, "forbidden: open the URL printed at startup", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, s.index)
}

// guard rejects requests that are not local or lack the per-launch UI token,
// which keeps other local processes and other browser origins out.
func (s *Server) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLocalHost(r.Host) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Header.Get("X-UI-Token") != s.uiToken {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

// ctx returns a per-request context with a sensible timeout.
func reqCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 45*time.Second)
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"loggedIn": s.client.Token() != "",
		"email":    s.loginEmail(),
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct{ Email, Password string }
	if !readJSON(w, r, &req) {
		return
	}
	if req.Email == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "email and password are required")
		return
	}
	ctx, cancel := reqCtx(r)
	defer cancel()
	res, err := s.client.Login(ctx, req.Email, req.Password, nil)
	if errors.Is(err, franklinwh.ErrMFARequired) {
		method := res.PreferredMFA()
		writeJSON(w, http.StatusOK, map[string]any{
			"mfaRequired": true,
			"mfaToken":    res.MFAToken,
			"method":      method,
			"methods":     res.AvailableMFA,
			"maskedEmail": res.MaskedEmail,
		})
		return
	}
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	s.saveLogin(req.Email)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMFASend(w http.ResponseWriter, r *http.Request) {
	var req struct{ MfaToken string }
	if !readJSON(w, r, &req) {
		return
	}
	ctx, cancel := reqCtx(r)
	defer cancel()
	if err := s.client.SendEmailOTP(ctx, req.MfaToken); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMFAVerify(w http.ResponseWriter, r *http.Request) {
	var req struct{ MfaToken, Method, Code, Email string }
	if !readJSON(w, r, &req) {
		return
	}
	ctx, cancel := reqCtx(r)
	defer cancel()
	if _, err := s.client.VerifyMFA(ctx, req.MfaToken, req.Method, strings.TrimSpace(req.Code), true); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	email := req.Email
	if email == "" {
		email = s.sessionEmail()
	}
	s.saveLogin(email)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	if s.client.Token() != "" {
		_ = s.client.Logout(ctx)
	}
	s.mu.Lock()
	s.cfg.Session.Token = ""
	_ = SaveSession(s.cfg.SessionPath, s.cfg.Session)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleGateways(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	gws, err := s.client.Gateways(ctx)
	if err != nil {
		s.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, gws)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	id, err := s.resolveGateway(ctx, r.URL.Query().Get("gateway"))
	if err != nil {
		s.apiError(w, err)
		return
	}
	st, err := s.client.Status(ctx, id)
	if err != nil {
		s.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// gridView is the grid-limits shape the page consumes.
type gridView struct {
	GatewayID     string  `json:"gatewayId"`
	ImportKW      float64 `json:"importKW"`
	ImportLimited bool    `json:"importLimited"`
	ExportKW      float64 `json:"exportKW"`
	ExportLimited bool    `json:"exportLimited"`
}

func (s *Server) handleGrid(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	switch r.Method {
	case http.MethodGet:
		id, err := s.resolveGateway(ctx, r.URL.Query().Get("gateway"))
		if err != nil {
			s.apiError(w, err)
			return
		}
		l, err := s.client.GridLimits(ctx, id)
		if err != nil {
			s.apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, gridViewOf(id, l))
	case http.MethodPost:
		var req struct {
			Gateway string
			Import  *float64
			Export  *float64
		}
		if !readJSON(w, r, &req) {
			return
		}
		id, err := s.resolveGateway(ctx, req.Gateway)
		if err != nil {
			s.apiError(w, err)
			return
		}
		if (req.Import != nil && *req.Import < 0) || (req.Export != nil && *req.Export < 0) {
			writeErr(w, http.StatusBadRequest, "limits must not be negative")
			return
		}
		after, err := s.client.UpdateGridLimits(ctx, id, req.Import, req.Export)
		if err != nil {
			s.apiError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, gridViewOf(id, after))
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func gridViewOf(id string, l *franklinwh.GridLimits) gridView {
	return gridView{
		GatewayID:     id,
		ImportKW:      l.ImportKW,
		ImportLimited: l.ImportFlag == franklinwh.GridLimitLimited,
		ExportKW:      l.ExportKW,
		ExportLimited: l.ExportFlag == franklinwh.GridLimitLimited,
	}
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	s.ping.Store(time.Now().UnixNano())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleQuit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	s.quit()
}

// resolveGateway returns the requested gateway or the first on the account.
func (s *Server) resolveGateway(ctx context.Context, id string) (string, error) {
	if id != "" {
		return id, nil
	}
	gw, _, err := s.client.DefaultGateway(ctx)
	if err != nil {
		return "", err
	}
	return gw.ID, nil
}

// apiError maps a client error to an HTTP response, flagging an expired
// session so the page can show the login form again.
func (s *Server) apiError(w http.ResponseWriter, err error) {
	if errors.Is(err, franklinwh.ErrUnauthorized) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "not logged in", "needLogin": true})
		return
	}
	if errors.Is(err, franklinwh.ErrRateLimited) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "FranklinWH is rate-limiting requests", "rateLimited": true})
		return
	}
	writeErr(w, http.StatusBadGateway, err.Error())
}

func (s *Server) saveLogin(email string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Session.Email = email
	s.cfg.Session.Token = s.client.Token()
	if err := SaveSession(s.cfg.SessionPath, s.cfg.Session); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not save session:", err)
	}
}

// loginEmail is the email to pre-fill on the login form.
func (s *Server) loginEmail() string {
	if e := s.sessionEmail(); e != "" {
		return e
	}
	return s.cfg.Email
}

func (s *Server) sessionEmail() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Session.Email
}

func (s *Server) quit() {
	s.shutdownOnce.Do(func() { close(s.done) })
}

// WatchHeartbeat closes Done when the page stops pinging (the tab was
// closed), so a double-clicked instance does not linger. It returns when
// Done is closed.
func (s *Server) WatchHeartbeat() {
	s.ping.Store(time.Now().UnixNano())
	const idle = 30 * time.Second
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			last := time.Unix(0, s.ping.Load())
			if time.Since(last) > idle {
				fmt.Fprintln(os.Stderr, "browser closed; shutting down")
				s.quit()
				return
			}
		}
	}
}

// --- small helpers ---

func randomToken() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func isLocalHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	return h == "127.0.0.1" || h == "localhost" || h == "::1" || h == "[::1]"
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}
