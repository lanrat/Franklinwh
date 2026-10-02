# franklinwh

An unofficial Go client and command-line tool for the **FranklinWH** cloud
API — the service behind the FranklinWH app (`com.Franklinwh.FamilyEnergy`)
that monitors aGate / aPower home energy systems. It lets you read your
**battery**, **grid** and **solar** status from your own scripts.

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

Requires Go 1.24+.

## Command-line usage

Credentials come from flags or environment variables:

| Variable | Flag | Purpose |
|---|---|---|
| `FRANKLINWH_EMAIL` | `-email` | account email |
| `FRANKLINWH_PASSWORD` | `-password` | account password (prompted if unset) |
| `FRANKLINWH_TOKEN` | `-token` | saved login token (skips login) |
| `FRANKLINWH_GATEWAY` | `-gateway` | gateway ID (defaults to the first) |

```sh
# Log in once and print a reusable token (handles MFA interactively)
export FRANKLINWH_EMAIL=you@example.com
franklinwh login
# -> prints a token; save it:
export FRANKLINWH_TOKEN=<that token>

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
code and `-token` lets you skip MFA on later runs (the client presents a stable
device ID and sets "remember this device").

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

## Disclaimer

Provided as-is for interoperability with hardware you own. Names and trademarks
belong to their respective owners. Review FranklinWH's terms before use.
