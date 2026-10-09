# franklinwh

An unofficial Go client, command-line tool, and local web dashboard for the
**FranklinWH** cloud API — the service behind the FranklinWH app
(`com.Franklinwh.FamilyEnergy`) that monitors aGate / aPower home energy
systems. It lets you see your **battery**, **grid** and **solar** status, and
read or set grid import/export limits, from a GUI, the command line, or your
own scripts.

The protocol was reverse engineered from version 2.23.0 of the Android app and
is documented in **[docs/API.md](docs/API.md)**.

> ⚠️ **Unofficial & unsupported.** This is not affiliated with FranklinWH. The
> API is undocumented and may change at any time. Use your own account, and
> poll gently (the app itself refreshes only about every 10 seconds).

## Install

```sh
# command-line tool
go install github.com/lanrat/franklinwh/cmd/franklinwh@latest

# or use it as a library
go get github.com/lanrat/franklinwh
```

Requires Go 1.24+. Pre-built Linux and Windows x64 binaries are attached to
each [release](https://github.com/lanrat/franklinwh/releases).

## Desktop dashboard (GUI)

Run `franklinwh` with **no arguments** to open a local dashboard in your
browser — login (with MFA), live battery/grid/solar status that refreshes
every ~10 s, and the grid import/export controls:

```sh
franklinwh            # opens http://127.0.0.1:<port>/ in your browser
```

- **Windows:** just **double-click `franklinwh.exe`** — it opens the dashboard
  with no console window. The same exe is still a full CLI from a terminal.
- It runs a small server bound to `127.0.0.1` only; every request carries a
  per-launch token, so other programs and websites can't reach it.
- Click **Quit** (or close the tab) to stop it. Flags: `-addr` to choose the
  listen address, `-no-browser` to not auto-open one.

The GUI makes the API calls from Go, so there is no browser CORS issue and
nothing is sent anywhere except FranklinWH's own servers.

## Command-line usage

Credentials come from flags or environment variables:

| Variable | Flag | Purpose |
|---|---|---|
| `FRANKLINWH_EMAIL` | `-email` | account email |
| `FRANKLINWH_PASSWORD` | `-password` | account password (prompted if unset) |
| `FRANKLINWH_TOKEN` | `-token` | login token to use instead of the saved session |
| `FRANKLINWH_GATEWAY` | `-gateway` | gateway ID (defaults to the first) |
| `FRANKLINWH_SESSION` | `-session` | session file (default `~/.config/franklinwh/session.json`; `-session=` disables) |

After a successful login the CLI saves the token, email and a stable client ID
to the session file (mode `0600`). Later runs reuse it, so you only enter the
password (and MFA code) again when the token expires; the CLI then logs in
again automatically.

```sh
# Log in once (handles MFA interactively); the session is saved
franklinwh -email you@example.com login

# Forget the saved session
franklinwh logout

# List your gateways
franklinwh gateways

# Battery / grid / solar summary
franklinwh status
```

Example `status` output:

```
Gateway:   10050001A02F22020077
Mode:      Time of Use
Battery:   87.5%  charging 0.500 kW
Grid:      on-grid   importing 1.200 kW
Solar:     3.400 kW
Home load: 2.100 kW
aPower units: 87.5%, 88.0%
```

Machine-readable output and full telemetry:

```sh
franklinwh -json status     # the summary as JSON
franklinwh raw              # the complete getDeviceCompositeInfo payload
```

If your account uses multi-factor auth, `franklinwh login` sends/validates the
code. The saved session then skips MFA on later runs, and because the client
ID is saved too, the server's "remember this device" applies on re-login.

## Library usage

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/lanrat/franklinwh"
)

func main() {
	ctx := context.Background()
	c := franklinwh.NewClient()

	_, err := c.Login(ctx, "you@example.com", "password", nil)
	if errors.Is(err, franklinwh.ErrMFARequired) {
		log.Fatal("account requires MFA; see franklinwh.Client.SendEmailOTP / VerifyMFA")
	} else if err != nil {
		log.Fatal(err)
	}

	gateways, err := c.Gateways(ctx)
	if err != nil {
		log.Fatal(err)
	}

	st, err := c.Status(ctx, gateways[0].ID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("battery %.1f%%, grid %+.3f kW, solar %.3f kW\n",
		st.Battery.SoC, st.Grid.PowerKW, st.Solar.PowerKW)
}
```

Save `c.Token()` after login and pass `franklinwh.WithToken(...)` next time to
avoid logging in repeatedly. For endpoints this package does not wrap, use
`Client.Do` with any path from [docs/API.md](docs/API.md).

### Key values and sign conventions

From `Status` (and the underlying `runtimeData`):

- **Battery** `PowerKW`: `< 0` charging, `> 0` discharging; `SoC` in %.
- **Grid** `PowerKW`: `> 0` importing, `< 0` exporting; `Connected` is on/off-grid.
- **Solar** `PowerKW`: production.
- **Home load** `HomeLoadKW`: consumption.

All power is in kW; energy counters in the raw data are kWh. See the
[field glossary](docs/API.md#runtimedata-field-glossary) for everything else.

## How it works

The app is a Flutter application that talks to a JSON HTTPS API. Login encrypts
the password with a scheme the server expects (documented in
[docs/API.md](docs/API.md#password-encryption)), then every call carries a
`loginToken` header. Live status comes from a single endpoint,
`getDeviceCompositeInfo`; lower-level/cell data is available through an MQTT
passthrough endpoint. The app can also talk to the gateway directly over the
local network (TCP), Bluetooth, or — if enabled — SunSpec Modbus TCP; those are
described in the docs but not implemented here.

## Development

```sh
go test ./...     # unit tests + mock-server integration tests
go build ./...
```

Tests run against an in-process fake server and do not need an account or
network access.

### Android app

`android/` is a minimal Android app that shows the same dashboard as the
desktop GUI in a WebView: the Go library runs the dashboard server inside the
app on a random loopback port. It needs Android 13 (API 33) or newer. CI
builds a debug-signed APK on every push (the `franklinwh-android` artifact).

To build it yourself (needs a JDK 17, the Android SDK and NDK):

```sh
scripts/build-aar.sh                      # Go library -> android/app/libs/franklinwh.aar
cd android && ./gradlew :app:assembleDebug
adb install app/build/outputs/apk/debug/app-debug.apk
```

`mobile/` is the [gomobile](https://pkg.go.dev/golang.org/x/mobile/cmd/gomobile)
package the app is built from. Besides `StartServer`, it offers a native API
whose methods return JSON documents described by `mobile/schema`, for a future
native UI ([docs/ANDROID_PORT.md](docs/ANDROID_PORT.md)).

### Releasing

Either push a tag (`git tag v1.2.3 && git push origin v1.2.3`), or open
**Actions → build → Run workflow** on `main` and enter the version. Both build
and test everything, then publish a GitHub Release with the Linux and Windows
binaries and the Android APK; the second also creates the tag.

## Disclaimer

Provided as-is for interoperability with hardware you own. Names and trademarks
belong to their respective owners. Review FranklinWH's terms before use.
