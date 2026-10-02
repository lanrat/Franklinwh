// Command franklinwh is a small command-line client for the FranklinWH
// cloud API. It can log in (handling MFA), list gateways, and print battery,
// grid and solar status as text or JSON.
//
// Credentials are taken from flags or the environment:
//
//	FRANKLINWH_EMAIL, FRANKLINWH_PASSWORD   login credentials
//	FRANKLINWH_TOKEN                        a saved login token (skips login)
//	FRANKLINWH_GATEWAY                      default gateway ID
//
// Examples:
//
//	franklinwh login                 # log in, print a token to reuse
//	franklinwh gateways              # list gateways
//	franklinwh status                # battery/grid/solar summary
//	franklinwh status -json          # same, as JSON
//	franklinwh raw                   # full getDeviceCompositeInfo JSON
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lanrat/franklinwh"
	"golang.org/x/term"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `franklinwh - unofficial FranklinWH CLI

Usage:
  franklinwh [global flags] <command> [flags]

Commands:
  login       Authenticate and print a reusable token
  gateways    List the gateways on the account
  status      Print battery, grid and solar status (default)
  raw         Print the full device telemetry JSON

Global flags:
  -email string      account email (or FRANKLINWH_EMAIL)
  -password string   account password (or FRANKLINWH_PASSWORD; prompts if unset)
  -token string      saved login token (or FRANKLINWH_TOKEN)
  -gateway string    gateway ID (or FRANKLINWH_GATEWAY; defaults to the first)
  -json              output JSON where supported
  -timeout duration  overall timeout (default 45s)

Environment variables are used when the matching flag is not set.
`)
}

func run(args []string) error {
	fs := flag.NewFlagSet("franklinwh", flag.ContinueOnError)
	fs.Usage = usage
	var (
		email    = fs.String("email", os.Getenv("FRANKLINWH_EMAIL"), "account email")
		password = fs.String("password", os.Getenv("FRANKLINWH_PASSWORD"), "account password")
		token    = fs.String("token", os.Getenv("FRANKLINWH_TOKEN"), "saved login token")
		gateway  = fs.String("gateway", os.Getenv("FRANKLINWH_GATEWAY"), "gateway ID")
		baseURL  = fs.String("base-url", os.Getenv("FRANKLINWH_BASE_URL"), "API base URL (advanced; defaults to the production endpoint)")
		asJSON   = fs.Bool("json", false, "output JSON where supported")
		timeout  = fs.Duration("timeout", 45*time.Second, "overall timeout")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cmd := "status"
	if fs.NArg() > 0 {
		cmd = fs.Arg(0)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := []franklinwh.Option{}
	if *token != "" {
		opts = append(opts, franklinwh.WithToken(*token))
	}
	if *baseURL != "" {
		opts = append(opts, franklinwh.WithBaseURL(*baseURL))
	}
	c := franklinwh.NewClient(opts...)

	// Ensure we are authenticated for every command except a pure token login.
	if c.Token() == "" {
		if err := doLogin(ctx, c, *email, password); err != nil {
			return err
		}
		if cmd == "login" {
			fmt.Println(c.Token())
			fmt.Fprintln(os.Stderr, "Save this token and pass it with -token or FRANKLINWH_TOKEN to skip login next time.")
			return nil
		}
	} else if cmd == "login" {
		fmt.Println(c.Token())
		return nil
	}

	switch cmd {
	case "gateways":
		return cmdGateways(ctx, c, *asJSON)
	case "status":
		return cmdStatus(ctx, c, *gateway, *asJSON)
	case "raw":
		return cmdRaw(ctx, c, *gateway)
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
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
	method := res.MFAMethod
	if method == "" && len(res.AvailableMFA) > 0 {
		method = res.AvailableMFA[0]
	}
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

// resolveGateway returns the requested gateway ID, or the first one on the
// account when none was given.
func resolveGateway(ctx context.Context, c *franklinwh.Client, gateway string) (string, error) {
	if gateway != "" {
		return gateway, nil
	}
	gws, err := c.Gateways(ctx)
	if err != nil {
		return "", err
	}
	if len(gws) == 0 {
		return "", errors.New("no gateways on this account")
	}
	if len(gws) > 1 {
		fmt.Fprintf(os.Stderr, "note: %d gateways found, using %s; pass -gateway to choose\n", len(gws), gws[0].ID)
	}
	return gws[0].ID, nil
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
