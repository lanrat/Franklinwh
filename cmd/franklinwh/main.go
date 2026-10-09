// Command franklinwh is a client for the FranklinWH cloud API. Run with no
// arguments (or on Windows, double-click the .exe) it opens a local web
// dashboard in the browser; run with a subcommand it works as a CLI that can
// log in (handling MFA), list gateways, print battery/grid/solar status, and
// read or set grid import/export limits.
//
// Credentials are taken from flags or the environment:
//
//	FRANKLINWH_EMAIL, FRANKLINWH_PASSWORD   login credentials
//	FRANKLINWH_TOKEN                        a saved login token (skips login)
//	FRANKLINWH_GATEWAY                      default gateway ID
//	FRANKLINWH_SESSION                      session file path
//
// After a successful login the token, client ID and email are saved to a
// session file (by default in the user config directory) and reused by later
// runs, so the password and MFA code are only needed again when the token
// expires.
//
// Examples:
//
//	franklinwh login                 # log in and save the session
//	franklinwh logout                # log out and delete the saved session
//	franklinwh gateways              # list gateways
//	franklinwh status                # battery/grid/solar summary
//	franklinwh status -json          # same, as JSON
//	franklinwh raw                   # full getDeviceCompositeInfo JSON
//	franklinwh grid                  # show grid import/export limits
//	franklinwh grid -import 5 -export 3   # set them (kW)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lanrat/franklinwh"
	"github.com/lanrat/franklinwh/internal/gui"
	"golang.org/x/term"
)

func main() {
	// On Windows the binary is a GUI-subsystem app; attach to the parent
	// console (if any) so CLI output still works from a terminal. No-op
	// elsewhere.
	attachConsole()
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `franklinwh - unofficial FranklinWH CLI

Usage:
  franklinwh [global flags] [command] [flags]

With no command it opens the graphical dashboard in your browser.

Commands:
  gui         Open the dashboard in a browser (default when no command given)
  login       Log in (always prompts), save the session and print the token
  logout      Invalidate the saved token and delete the session file
  gateways    List the gateways on the account
  status      Print battery, grid and solar status
  raw         Print the full device telemetry JSON
  grid        Show grid import/export limits; set them with
              grid [-import kW] [-export kW] [-dry-run]

Global flags:
  -email string      account email (or FRANKLINWH_EMAIL)
  -password string   account password (or FRANKLINWH_PASSWORD; prompts if unset)
  -token string      login token to use instead of the session (or FRANKLINWH_TOKEN)
  -session string    session file (or FRANKLINWH_SESSION; default
                     $XDG_CONFIG_HOME/franklinwh/session.json); "-session=" disables it
  -gateway string    gateway ID (or FRANKLINWH_GATEWAY; defaults to the first)
  -json              output JSON where supported
  -timeout duration  overall timeout (default 45s)
  -addr string       gui: address to listen on (default 127.0.0.1:0)
  -no-browser        gui: do not open a browser automatically

Environment variables are used when the matching flag is not set.
`)
}

func run(args []string) error {
	fs := flag.NewFlagSet("franklinwh", flag.ContinueOnError)
	fs.Usage = usage
	var (
		email     = fs.String("email", os.Getenv("FRANKLINWH_EMAIL"), "account email")
		password  = fs.String("password", os.Getenv("FRANKLINWH_PASSWORD"), "account password")
		token     = fs.String("token", os.Getenv("FRANKLINWH_TOKEN"), "saved login token")
		gateway   = fs.String("gateway", os.Getenv("FRANKLINWH_GATEWAY"), "gateway ID")
		session   = fs.String("session", envOr("FRANKLINWH_SESSION", defaultSessionPath()), "session file (empty disables)")
		baseURL   = fs.String("base-url", os.Getenv("FRANKLINWH_BASE_URL"), "API base URL (advanced; defaults to the production endpoint)")
		asJSON    = fs.Bool("json", false, "output JSON where supported")
		timeout   = fs.Duration("timeout", 45*time.Second, "overall timeout")
		addr      = fs.String("addr", "127.0.0.1:0", "gui: address to listen on")
		noBrowser = fs.Bool("no-browser", false, "gui: do not open a browser")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	// With no command (e.g. a double-click on Windows) open the GUI.
	cmd := "gui"
	if fs.NArg() > 0 {
		cmd = fs.Arg(0)
	}

	if cmd == "gui" {
		return runGUI(gui.Config{
			BaseURL:     *baseURL,
			Addr:        *addr,
			SessionPath: *session,
			Session:     gui.LoadSession(*session),
			Email:       *email,
		}, !*noBrowser)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	sess := gui.LoadSession(*session)
	if *email == "" {
		*email = sess.Email
	}
	if sess.ClientID == "" {
		// A stable client ID lets MFA's "remember this device" take effect.
		sess.ClientID = franklinwh.NewClientID()
	}

	opts := []franklinwh.Option{franklinwh.WithClientID(sess.ClientID)}
	if *baseURL != "" {
		opts = append(opts, franklinwh.WithBaseURL(*baseURL))
	}
	// Prefer an explicit token; otherwise reuse the saved one if it belongs
	// to the requested account.
	fromSession := false
	switch {
	case *token != "":
		opts = append(opts, franklinwh.WithToken(*token))
	case sess.Token != "" && strings.EqualFold(sess.Email, *email) && cmd != "login":
		opts = append(opts, franklinwh.WithToken(sess.Token))
		fromSession = true
	}
	c := franklinwh.NewClient(opts...)

	login := func() error {
		if err := doLogin(ctx, c, *email, password); err != nil {
			return err
		}
		sess.Email, sess.Token = *email, c.Token()
		if err := gui.SaveSession(*session, sess); err != nil {
			fmt.Fprintln(os.Stderr, "warning: could not save session:", err)
		}
		return nil
	}

	switch cmd {
	case "login":
		if *token == "" {
			if err := login(); err != nil {
				return err
			}
			if *session != "" {
				fmt.Fprintf(os.Stderr, "Session saved to %s\n", *session)
			}
		}
		fmt.Println(c.Token())
		return nil
	case "logout":
		if c.Token() != "" {
			if err := c.Logout(ctx); err != nil {
				fmt.Fprintln(os.Stderr, "warning: server logout failed:", err)
			}
		}
		if *session != "" {
			if err := os.Remove(*session); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return nil
	}

	var run func() error
	switch cmd {
	case "gateways":
		run = func() error { return cmdGateways(ctx, c, *asJSON) }
	case "status":
		run = func() error { return cmdStatus(ctx, c, *gateway, *asJSON) }
	case "raw":
		run = func() error { return cmdRaw(ctx, c, *gateway) }
	case "grid":
		gfs := flag.NewFlagSet("grid", flag.ContinueOnError)
		imp := gfs.String("import", "", "grid import limit in kW")
		exp := gfs.String("export", "", "grid export limit in kW")
		dry := gfs.Bool("dry-run", false, "show the change without sending it")
		if err := gfs.Parse(fs.Args()[1:]); err != nil {
			return err
		}
		run = func() error { return cmdGrid(ctx, c, *gateway, *imp, *exp, *dry, *asJSON) }
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}

	if c.Token() == "" {
		if err := login(); err != nil {
			return err
		}
	}
	err := run()
	if fromSession && errors.Is(err, franklinwh.ErrUnauthorized) {
		// The saved token expired: log in again and retry once.
		fmt.Fprintln(os.Stderr, "Saved session expired; logging in again.")
		c.SetToken("")
		if err := login(); err != nil {
			return err
		}
		err = run()
	}
	return err
}

// defaultSessionPath returns the session file location in the user config
// directory, or "" (sessions disabled) if there is none.
func defaultSessionPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "franklinwh", "session.json")
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// doLogin authenticates the client, prompting for a password and for an MFA
// code when needed.
func doLogin(ctx context.Context, c *franklinwh.Client, email string, password *string) error {
	if email == "" {
		return errors.New("no email: set -email or FRANKLINWH_EMAIL (or provide -token)")
	}
	if *password == "" {
		pw, err := promptSecret("FranklinWH password: ")
		if err != nil {
			return err
		}
		*password = pw
	}

	res, err := c.Login(ctx, email, *password, nil)
	if err == nil {
		return nil
	}
	if !errors.Is(err, franklinwh.ErrMFARequired) {
		return err
	}

	// MFA required.
	method := res.PreferredMFA()
	if method == franklinwh.MFAEmailOTP {
		if err := c.SendEmailOTP(ctx, res.MFAToken); err != nil {
			return fmt.Errorf("sending email OTP: %w", err)
		}
		fmt.Fprintf(os.Stderr, "A code was emailed to %s.\n", res.MaskedEmail)
	}
	code, err := promptLine(fmt.Sprintf("MFA code (%s): ", method))
	if err != nil {
		return err
	}
	if _, err := c.VerifyMFA(ctx, res.MFAToken, method, strings.TrimSpace(code), true); err != nil {
		return fmt.Errorf("verifying MFA: %w", err)
	}
	return nil
}

func cmdGateways(ctx context.Context, c *franklinwh.Client, asJSON bool) error {
	gws, err := c.Gateways(ctx)
	if err != nil {
		return err
	}
	if asJSON {
		return printJSON(gws)
	}
	if len(gws) == 0 {
		fmt.Println("no gateways found")
		return nil
	}
	for _, g := range gws {
		name := g.Name
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Printf("%s  %s  protocol=%s version=%s\n", g.ID, name, g.ProtocolVer, g.Version)
	}
	return nil
}

func cmdStatus(ctx context.Context, c *franklinwh.Client, gateway string, asJSON bool) error {
	id, err := resolveGateway(ctx, c, gateway)
	if err != nil {
		return err
	}
	st, err := c.Status(ctx, id)
	if err != nil {
		return err
	}
	if asJSON {
		return printJSON(st)
	}
	printStatus(st)
	return nil
}

func cmdRaw(ctx context.Context, c *franklinwh.Client, gateway string) error {
	id, err := resolveGateway(ctx, c, gateway)
	if err != nil {
		return err
	}
	info, err := c.GetDeviceCompositeInfo(ctx, id, true)
	if err != nil {
		return err
	}
	return printJSON(info)
}

func cmdGrid(ctx context.Context, c *franklinwh.Client, gateway, imp, exp string, dryRun, asJSON bool) error {
	id, err := resolveGateway(ctx, c, gateway)
	if err != nil {
		return err
	}
	l, err := c.GridLimits(ctx, id)
	if err != nil {
		return err
	}
	if imp == "" && exp == "" {
		if asJSON {
			return printJSON(l.Raw)
		}
		printGridLimits(l)
		return nil
	}

	impKW, err := parseLimit("import", imp)
	if err != nil {
		return err
	}
	expKW, err := parseLimit("export", exp)
	if err != nil {
		return err
	}
	before := *l
	if err := l.Apply(impKW, expKW); err != nil {
		return err
	}
	fmt.Println("Current:")
	printGridLimits(&before)
	fmt.Println("New:")
	printGridLimits(l)
	if dryRun {
		fmt.Println("(dry run, nothing sent)")
		return nil
	}
	after, err := c.UpdateGridLimits(ctx, id, impKW, expKW)
	if after != nil {
		fmt.Println("Saved; the gateway now reports:")
		printGridLimits(after)
	}
	return err
}

// parseLimit parses a kW value for GridLimits.Apply. An empty value returns
// nil, leaving the limit unchanged.
func parseLimit(name, v string) (*float64, error) {
	if v == "" {
		return nil, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return nil, fmt.Errorf("-%s: want a non-negative number of kW, got %q", name, v)
	}
	return &f, nil
}

func printGridLimits(l *franklinwh.GridLimits) {
	fmt.Printf("  Import from grid: %s\n", limitString(l.ImportKW, l.ImportFlag))
	fmt.Printf("  Export to grid:   %s\n", limitString(l.ExportKW, l.ExportFlag))
}

func limitString(kw float64, flagVal int) string {
	if flagVal == franklinwh.GridLimitLimited {
		return fmt.Sprintf("%.2f kW", kw)
	}
	return fmt.Sprintf("%.2f kW (flag %d, meaning unconfirmed)", kw, flagVal)
}

// resolveGateway returns the requested gateway ID, or the first one on the
// account when none was given.
func resolveGateway(ctx context.Context, c *franklinwh.Client, gateway string) (string, error) {
	if gateway != "" {
		return gateway, nil
	}
	gw, total, err := c.DefaultGateway(ctx)
	if err != nil {
		return "", err
	}
	if total > 1 {
		fmt.Fprintf(os.Stderr, "note: %d gateways found, using %s; pass -gateway to choose\n", total, gw.ID)
	}
	return gw.ID, nil
}

func printStatus(st *franklinwh.Status) {
	fmt.Printf("Gateway:   %s\n", st.GatewayID)
	fmt.Printf("Mode:      %s\n", st.Mode)
	fmt.Printf("Battery:   %.1f%%  %s %.3f kW\n",
		st.Battery.SoC, batteryWord(st.Battery.PowerKW), abs(st.Battery.PowerKW))
	fmt.Printf("Grid:      %s  %s %.3f kW\n",
		connWord(st.Grid.Connected), chargeWord(st.Grid.PowerKW, "importing", "exporting", "idle"), abs(st.Grid.PowerKW))
	fmt.Printf("Solar:     %.3f kW\n", st.Solar.PowerKW)
	fmt.Printf("Home load: %.3f kW\n", st.HomeLoadKW)
	if st.Generator.Running || st.Generator.PowerKW != 0 {
		fmt.Printf("Generator: %.3f kW\n", st.Generator.PowerKW)
	}
	if n := len(st.Battery.PerUnitSoC); n > 0 {
		fmt.Printf("aPower units: ")
		for i, soc := range st.Battery.PerUnitSoC {
			if i > 0 {
				fmt.Print(", ")
			}
			fmt.Printf("%.1f%%", soc)
		}
		fmt.Println()
	}
}

// batteryWord describes battery power, where negative means charging.
func batteryWord(v float64) string {
	switch {
	case v < -0.0005:
		return "charging"
	case v > 0.0005:
		return "discharging"
	default:
		return "idle"
	}
}

func chargeWord(v float64, pos, neg, zero string) string {
	switch {
	case v > 0.0005:
		return pos
	case v < -0.0005:
		return neg
	default:
		return zero
	}
}

func connWord(connected bool) string {
	if connected {
		return "on-grid "
	}
	return "off-grid"
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// promptSecret reads a line without echoing it, falling back to a plain read
// when stdin is not a terminal (e.g. piped input).
func promptSecret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	return promptLine("")
}

func promptLine(prompt string) (string, error) {
	if prompt != "" {
		fmt.Fprint(os.Stderr, prompt)
	}
	var s string
	if _, err := fmt.Scanln(&s); err != nil {
		return "", fmt.Errorf("reading input: %w", err)
	}
	return s, nil
}
