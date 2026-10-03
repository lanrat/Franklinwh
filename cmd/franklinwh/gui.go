package main

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
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/lanrat/franklinwh"
)

//go:embed web/index.html
var indexHTML string

// guiConfig holds what the GUI server needs from the command line.
type guiConfig struct {
	addr        string  // listen address; default 127.0.0.1:0 (random port)
	baseURL     string  // API base URL override, if any
	sessionPath string  // where to persist the login session
	sess        session // session loaded at startup
	email       string  // pre-fill for the login form
	openBrowser bool    // open the default browser on start
}

// guiServer serves the local dashboard and proxies to the FranklinWH API.
type guiServer struct {
	cfg     guiConfig
	client  *franklinwh.Client
	uiToken string // required on every /api call; embedded in the page
	index   string // index.html with the token injected

	mu   sync.Mutex // guards session writes
	ping atomic.Int64

	shutdownOnce sync.Once
	done         chan struct{}
}

// runGUI starts the local web UI and blocks until the user quits (via the
// page, Ctrl+C, or the browser going away).
func runGUI(cfg guiConfig) error {
	tok, err := randomToken()
	if err != nil {
		return err
	}
	opts := []franklinwh.Option{franklinwh.WithClientID(cfg.sess.ClientID)}
	if cfg.baseURL != "" {
		opts = append(opts, franklinwh.WithBaseURL(cfg.baseURL))
	}
	if cfg.sess.Token != "" {
		opts = append(opts, franklinwh.WithToken(cfg.sess.Token))
	}
	s := &guiServer{
		cfg:     cfg,
		client:  franklinwh.NewClient(opts...),
		uiToken: tok,
		index:   strings.ReplaceAll(indexHTML, "__UI_TOKEN__", tok),
		done:    make(chan struct{}),
	}
	if cfg.email == "" {
		cfg.email = cfg.sess.Email
	}

	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("starting GUI server: %w", err)
	}
	url := "http://" + ln.Addr().String() + "/"

	srv := &http.Server{Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "GUI server error:", err)
			s.quit()
		}
	}()

	fmt.Fprintf(os.Stderr, "FranklinWH dashboard running at %s\n", url)
	if cfg.openBrowser {
		s.ping.Store(time.Now().UnixNano())
		go s.watchHeartbeat()
		if err := openBrowser(url); err != nil {
			fmt.Fprintf(os.Stderr, "could not open a browser automatically; open %s yourself\n", url)
		}
	} else {
		fmt.Fprintf(os.Stderr, "open %s in your browser\n", url)
	}

	select {
	case <-ctx.Done():
	case <-s.done:
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return srv.Shutdown(shutCtx)
}

func (s *guiServer) routes() http.Handler {
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

func (s *guiServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, s.index)
}

// guard rejects requests that are not local or lack the per-launch UI token,
// which keeps other local processes and other browser origins out.
func (s *guiServer) guard(h http.HandlerFunc) http.HandlerFunc {
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

func (s *guiServer) handleSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"loggedIn": s.client.Token() != "",
		"email":    s.sessionEmail(),
	})
}

func (s *guiServer) handleLogin(w http.ResponseWriter, r *http.Request) {
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
		method := res.MFAMethod
		if method == "" && len(res.AvailableMFA) > 0 {
			method = res.AvailableMFA[0]
		}
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

func (s *guiServer) handleMFASend(w http.ResponseWriter, r *http.Request) {
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

func (s *guiServer) handleMFAVerify(w http.ResponseWriter, r *http.Request) {
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

func (s *guiServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	if s.client.Token() != "" {
		_ = s.client.Logout(ctx)
	}
	s.mu.Lock()
	s.cfg.sess.Token = ""
	_ = saveSession(s.cfg.sessionPath, s.cfg.sess)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *guiServer) handleGateways(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r)
	defer cancel()
	gws, err := s.client.Gateways(ctx)
	if err != nil {
		s.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, gws)
}

func (s *guiServer) handleStatus(w http.ResponseWriter, r *http.Request) {
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

func (s *guiServer) handleGrid(w http.ResponseWriter, r *http.Request) {
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
		l, err := s.client.GridLimits(ctx, id)
		if err != nil {
			s.apiError(w, err)
			return
		}
		if req.Import != nil {
			if *req.Import < 0 {
				writeErr(w, http.StatusBadRequest, "import limit must not be negative")
				return
			}
			l.ImportKW, l.ImportFlag = *req.Import, franklinwh.GridLimitLimited
		}
		if req.Export != nil {
			if *req.Export < 0 {
				writeErr(w, http.StatusBadRequest, "export limit must not be negative")
				return
			}
			l.ExportKW, l.ExportFlag = *req.Export, franklinwh.GridLimitLimited
		}
		if err := s.client.SetGridLimits(ctx, id, l); err != nil {
			s.apiError(w, err)
			return
		}
		after, err := s.client.GridLimits(ctx, id)
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

func (s *guiServer) handlePing(w http.ResponseWriter, r *http.Request) {
	s.ping.Store(time.Now().UnixNano())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *guiServer) handleQuit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	s.quit()
}

// resolveGateway returns the requested gateway or the first on the account.
func (s *guiServer) resolveGateway(ctx context.Context, id string) (string, error) {
	if id != "" {
		return id, nil
	}
	gws, err := s.client.Gateways(ctx)
	if err != nil {
		return "", err
	}
	if len(gws) == 0 {
		return "", errors.New("no gateways on this account")
	}
	return gws[0].ID, nil
}

// apiError maps a client error to an HTTP response, flagging an expired
// session so the page can show the login form again.
func (s *guiServer) apiError(w http.ResponseWriter, err error) {
	if errors.Is(err, franklinwh.ErrUnauthorized) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "not logged in", "needLogin": true})
		return
	}
	writeErr(w, http.StatusBadGateway, err.Error())
}

func (s *guiServer) saveLogin(email string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.sess.Email = email
	s.cfg.sess.Token = s.client.Token()
	if err := saveSession(s.cfg.sessionPath, s.cfg.sess); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not save session:", err)
	}
}

func (s *guiServer) sessionEmail() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.sess.Email
}

func (s *guiServer) quit() {
	s.shutdownOnce.Do(func() { close(s.done) })
}

// watchHeartbeat shuts the server down when the page stops pinging (the tab
// was closed), so a double-clicked instance does not linger.
func (s *guiServer) watchHeartbeat() {
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

// openBrowser opens url in the user's default browser.
func openBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	case "darwin":
		cmd, args = "open", []string{url}
	default:
		cmd, args = "xdg-open", []string{url}
	}
	return exec.Command(cmd, args...).Start()
}
