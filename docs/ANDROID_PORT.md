# Android port plan

A plan for shipping the FranklinWH dashboard as a native Android app, built on
the existing Go client.

> **Status:** phases 0–1 are done; phase 2 is built but not yet tested on a
> device with a real account. Phase 2 was cut down to the WebView
> shortcut (option C below): `android/` is one Activity that starts the
> desktop dashboard's server in-process (`mobile.StartServer`) and loads it
> in a WebView, so login, MFA, status and grid limits all work through the
> existing page. The native Compose screens in phases 3–4 would replace the
> WebView; the `mobile` JSON API is ready for them.

## 1. What exists today

| Piece | Files | Size | What it does |
|---|---|---|---|
| Library | `franklinwh.go`, `auth.go`, `request.go`, `status.go`, `grid.go` | ~980 LOC | The reverse-engineered API client. It covers login (MD5 or AES password, form-encoded), MFA (TOTP and email OTP), the app's headers, the lenient envelope decoding, gateways, live telemetry, and reading and writing grid limits. Writing grid limits sends the `2.6.2` version header. |
| CLI | `cmd/franklinwh/main.go` | ~580 LOC | Subcommands, prompts, and a session file holding the token, client ID and email. |
| Local GUI | `cmd/franklinwh/gui.go`, `web/index.html` | ~870 LOC | A localhost HTTP server with a per-launch token. A single-page app polls `/api/status` every 10 s and edits the grid limits. |
| Tests | `*_test.go` | ~440 LOC | `httptest`-based tests for the client. |
| Docs | `docs/API.md`, `docs/endpoints.txt` | | The protocol reference. |

The library has no dependencies outside the standard library. It is
context-aware, concurrency-safe, and already has the hooks a mobile host
needs: `WithDevice`, `WithClientID`, `WithToken`, `WithHTTPClient` and
`WithBaseURL`. Everything in `gui.go` that is specific to a desktop computer
(the localhost server, the UI token, the browser launch, the heartbeat
shutdown) has no use on Android.

### Features to carry over (parity target = the web GUI)

1. Sign in with email and password, then MFA if the account needs it: pick a
   method, send an email OTP, enter the code, and "remember this device"
   (always on in the GUI).
2. Keep the session (token, client ID, email). Reuse it silently, and go back
   to the login screen on `ErrUnauthorized`.
3. Gateway picker. The CLI and GUI default to the first gateway.
4. Live status: battery SoC and kW, charging state, per-aPower units, grid kW,
   import/export and connected state, solar, generator, home load, and mode.
   It refreshes every ~10 s while visible.
5. Grid import/export limits. Show them, and set them with validation
   (≥ 0, flag set to `GridLimitLimited`).
6. Log out.

## 2. Approach options

| Option | Reuse | Effort | Risk | Verdict |
|---|---|---|---|---|
| **A. `gomobile bind` the Go library → AAR, native Kotlin + Jetpack Compose UI** | All the protocol code | Medium | Gomobile type limits, an AAR of about 8–15 MB per ABI, an NDK in CI | **Recommended** |
| B. Rewrite the client in Kotlin (OkHttp + kotlinx.serialization) | Only the docs | Medium–high | Two copies of a fragile, reverse-engineered protocol to keep in sync (password encryption, form-only login, version-header spoofing, lenient decoding) | Fallback only |
| C. WebView around `index.html`, with the Go GUI server running in-process on localhost | All of it | Low | A localhost server inside an app, a non-native UX, awkward lifecycle, no widgets | Prototype only |

The protocol is undocumented and changes over time. Option A gives it a single
source of truth: a fix lands once in Go and reaches the CLI, the desktop GUI
and the Android app together. The UI is the only new code, and Compose is the
right tool for it.

## 3. Target architecture (Option A)

```
franklinwh/                 (existing Go module)
├── *.go                    library, mostly unchanged
├── mobile/                 NEW: gomobile-friendly facade (package mobile)
│   ├── mobile.go
│   └── mobile_test.go
├── cmd/franklinwh/         unchanged CLI + desktop GUI
└── android/                NEW: Gradle project
    ├── settings.gradle.kts
    ├── gradle/libs.versions.toml
    └── app/
        ├── libs/franklinwh.aar        (built by gomobile, not committed)
        └── src/main/java/.../
            ├── data/   FranklinRepository (wraps the AAR), SessionStore
            ├── ui/     login/, mfa/, dashboard/, grid/, settings/ (Compose)
            ├── widget/ Glance home-screen widget (phase 4)
            └── App.kt, MainActivity.kt
```

### 3.1 Go: small library refactors first (done)

- **Grid update logic is in the library.** `GridLimits.Apply(importKW, exportKW *float64)`
  validates ≥ 0 and sets `GridLimitLimited`. `Client.UpdateGridLimits` reads,
  applies, writes, re-reads, and returns `ErrGridLimitsNotApplied` when the
  server keeps the old values. The CLI and the GUI both use them.
- **`Client.DefaultGateway(ctx)`** returns the first gateway and the total
  count. It replaces the copies of `resolveGateway` in `main.go` and `gui.go`.
- **`LoginResult.PreferredMFA()`** picks `MFAMethod`, or else
  `AvailableMFA[0]`.

### 3.2 Go: the `mobile` facade package (done)

Gomobile only exports signed ints, floats, `string`, `bool`, `[]byte`, and
structs or interfaces built from those. It cannot export `context.Context`,
slices of structs, maps, or `json.Number`. The facade therefore stays thin and
**exchanges JSON strings** for anything structured. The documents are defined
in `mobile/schema`, a separate package so that gobind doesn't try to bind
them. Kotlin mirrors them as kotlinx.serialization data classes.

The Java API that gobind generates (package `com.github.lanrat.franklinwh.mobile`):

```java
Config cfg = new Config();          // all optional: BaseURL, Token, ClientID,
cfg.setToken(saved);                // DeviceModel, DeviceName, OSVersion,
Client c = new Client(cfg);         // Language, TimeoutSeconds (default 45)

c.token(); c.setToken(t); c.clientID();   // persist token + clientID
c.cancel();                               // abort calls in flight

String login(email, password, timezone)   // schema.LoginResult {mfaRequired, mfaToken, method, methods, maskedEmail}
void   sendEmailOTP(mfaToken)
void   verifyMFA(mfaToken, method, code, remember)
void   logout()

String gateways()                         // [schema.Gateway]
String status(gatewayID)                  // schema.Status ("" = first gateway)
String rawStatus(gatewayID)               // unmodified getDeviceCompositeInfo result
String gridLimits(gatewayID)              // schema.GridLimits
String updateGridLimits(gatewayID, importKW, exportKW)  // Double.NaN = unchanged; result has "applied"

Mobile.errorKind(exception.getMessage())  // Mobile.ErrKindUnauthorized, ErrKindNetwork, ...
```

Notes:

- **Blocking calls.** Every method blocks, with a per-call timeout. Kotlin calls
  them on `Dispatchers.IO`. `cancel()` aborts everything in flight, for
  example from `ViewModel.onCleared`.
- **Errors.** Gomobile turns a Go `error` into a Java `Exception` and keeps only
  the message, so every message starts with a stable kind (`unauthorized:`,
  `network:`, `api:`, `invalid:`, `no_gateways:`, `canceled:`, `error:`), and
  `Mobile.errorKind` parses it.
- **Grid writes that don't take.** When the server accepts a change but reads
  back the old limits, `updateGridLimits` returns them with
  `"applied": false` rather than throwing, so the UI can show what the
  gateway really has.
- **Tests.** `mobile_test.go` runs against an `httptest` server, in the same
  way as `client_test.go`.

### 3.3 Android app

- **Stack.** Kotlin, Jetpack Compose with Material 3 (dynamic color and dark
  mode for free), a ViewModel and StateFlow per screen, and Navigation-Compose.
  Use Hilt or plain manual DI; the app is small, so manual DI is fine.
- **minSdk 33 (Android 13, what the Pixel 7 shipped with), targetSdk the
  current release.** Build only `arm64-v8a`: the
  supported phones are the Pixel 7 and newer, which are 64-bit ARM only. Use
  an arm64 emulator image, or set `TARGETS=android/arm64,android/amd64` to
  add x86_64.
- **`FranklinRepository`.** It owns the single `mobile.Client`, moves calls to
  `Dispatchers.IO`, decodes the JSON into data classes, and turns
  `unauthorized:` into a `SessionExpired` event that the nav graph sends to
  Login.
- **`SessionStore`.** It stores the token, client ID and email in DataStore.
  The token is encrypted with an AES-GCM key held in the Android Keystore, so
  the plaintext token never sits in shared prefs. (`EncryptedSharedPreferences`
  from security-crypto is deprecated, so don't use it.) Keep the client ID
  stable across logouts, because MFA "remember device" depends on it.
  Clear the token only on logout, which matches `gui.go:handleLogout`.
- **Device identity.** Pass `Build.MODEL`, the user-visible device name and
  `Build.VERSION.RELEASE` to `WithDevice`, and pass
  `TimeZone.getDefault().id` as the login timezone.
- **Polling.** A coroutine loops every 10 s and only while the screen is at
  least STARTED (`repeatOnLifecycle`), which matches the GUI's
  `setInterval(refresh, 10000)` and the README's "poll gently" note. Add
  pull-to-refresh, and back off exponentially on network errors.
- **Screens.**
  1. *Login*: email and password, with a progress indicator and an error
     snackbar.
  2. *MFA*: a method chooser (TOTP or email OTP), a "Send code" button for
     email, a code field with `KeyboardType.NumberPassword` and SMS/OTP
     autofill hints, and a "Remember this device" checkbox that defaults to on.
  3. *Dashboard*: a power-flow card (solar → home ← grid, battery ↔), a battery
     SoC gauge, per-aPower rows, a mode chip, a grid-connected badge, and a
     last-updated time. Choose the gateway from the top app bar when the
     account has more than one.
  4. *Grid limits*: current import and export limits with "No limit" shown for
     -1, two numeric fields, and a confirm dialog before writing. The dialog
     says the change goes to the live system.
  5. *Settings*: the account email, the gateway, a logout button, and an
     advanced base URL override (debug builds only).
- **Network.** Only HTTPS to `energy.franklinwh.com`. Add a
  `network_security_config` that disables cleartext and `INTERNET` as the only
  permission.

### 3.4 Nice-to-haves after parity (phase 4)

- A **home-screen widget** (Glance) showing SoC, grid and solar, updated by
  WorkManager. The minimum period is 15 min, so it shows "as of" times.
- A **Quick Settings tile** for the battery SoC.
- **Notifications** for grid outages (`Grid.Connected` turns false) or low SoC.
  These need a periodic worker and therefore cost battery and server polling,
  so make them opt-in.
- A raw JSON debug screen (`GetDeviceCompositeInfo`, like `franklinwh raw`)
  for reporting protocol changes.

## 4. Build & CI

Add an `android` job to `.github/workflows/build.yml` that runs after `test`:

1. `actions/setup-go` (from `go.mod`), `actions/setup-java` (Temurin 17), and
   `android-actions/setup-android`. Install an NDK version pinned in
   `android/gradle.properties`.
2. Run `go install golang.org/x/mobile/cmd/gomobile@<pinned>` and then
   `gomobile init`.
3. Run
   `gomobile bind -target=android/arm64 -androidapi 33 -trimpath -ldflags="-s -w" -o android/app/libs/franklinwh.aar ./mobile`.
4. Run `./gradlew :app:testDebugUnitTest :app:assembleRelease` (or
   `bundleRelease`).
5. On `v*` tags, sign the APK with a keystore stored in repo secrets and attach
   it to the GitHub Release next to the Linux and Windows binaries.

Developers get the same steps through a `make android` target or a
`scripts/build-aar.sh` script. The `.aar` is listed in `.gitignore`.

`golang.org/x/mobile` is pinned in `go.mod` with `tool` directives, at the last
commit that still supports Go 1.24. `scripts/build-aar.sh` installs gomobile
and gobind from those versions; don't run `gomobile init`, which installs
`gobind@latest`. The `android` CI job builds the AAR and then the APK. Only
the APK is published; the AAR is just an intermediate build step.

## 5. Phased plan

| Phase | Deliverable | Done when |
|---|---|---|
| 0. Library prep ✅ | `UpdateGridLimits`, `DefaultGateway` and `PreferredMFA` in the library; the CLI and GUI switched to them | `go test ./...` passes, and the CLI and GUI behave as before |
| 1. Go facade ✅ | The `mobile/` package and its tests, plus a local `gomobile bind` that produces an AAR | `go test ./mobile` passes, and the AAR builds (arm64 only since v0.2.0) |
| 2. App skeleton + auth (WebView; built, untested on device) | A Gradle project, `SessionStore`, and the Login and MFA screens | You can log in with real credentials (TOTP and email OTP), and the session survives an app restart |
| 3. Dashboard | Status polling, the gateway picker, and session-expired handling | The values match `franklinwh status` and polling pauses in the background |
| 4. Grid limits + settings | The read/write screen and logout | You can set and read back limits on a real gateway, and validation rejects negative values |
| 5. CI + release (signing ✅) | The Android job and a signed APK on tag | A tagged build publishes an APK |
| 6. Extras (optional) | A widget, a QS tile, alerts and a debug screen | — |

Phases 0–1 are Go-only and can merge on their own. Phases 2–4 are the bulk of
the new code, roughly 1.5–2.5k lines of Kotlin.

## 6. Testing

- **Go.** Unit tests for the new library helpers and the facade, written with
  `httptest`.
- **Kotlin unit tests.** Test the ViewModels against a fake `FranklinRepository`
  that sits behind an interface: the MFA branching, the session-expired
  redirect, and the polling backoff. Test JSON decoding against fixture
  payloads copied from `docs/API.md`.
- **Instrumented tests.** Compose UI tests for the login, MFA and grid forms.
  Add one smoke test that loads the real AAR and calls `NewClient` and
  `Token()`, so JNI or ABI packaging problems are caught.
- **Manual.** Run against a real account on a Pixel 7 or newer.

## 7. Risks & open questions

- **API drift and blocking.** The server already rejects some writes from the
  current app version, which is why the grid-limit write sends version
  `2.6.2`. A mobile app is more visible than a CLI. Keep the polling interval
  ≥ 10 s and make the client easy to update.
- **Name and trademark.** Don't call the app "FranklinWH" or use their logo or
  package name. Pick a neutral name and add an "unofficial" disclaimer on the
  login screen. Google Play may reject an app built on an unofficial API, so
  GitHub Releases (and possibly F-Droid) is the realistic distribution path.
- **F-Droid.** Reproducible gomobile builds need pinned Go, NDK and gomobile
  versions. That is doable but adds work, so treat it as optional.
- **DNS and TLS inside Go on Android.** Gomobile builds with cgo, so name
  resolution goes through bionic's `getaddrinfo`. Go reads the system CA store
  from `/system/etc/security/cacerts`. User-installed CAs and a VPN's private
  DNS can behave differently from OkHttp. If that causes problems, use
  `WithHTTPClient` to supply a Go `http.RoundTripper` that is backed by a
  Kotlin interface wrapping OkHttp.
- **APK size.** The Go runtime adds several MB per ABI. Per-ABI splits or an
  AAB keep each download to about 5–8 MB.
- **Credential handling.** The app never stores the password, only the token,
  encrypted with the Keystore. The password is sent as MD5 because the server
  requires it, as documented in `docs/API.md`. Note this in the README.
- **Open question.** Should the app support writing grid limits at all, given
  that FranklinWH's own app now treats them as read-only for homeowners? The
  options are to hide the write behind an "advanced" toggle, or to ship
  read-only first.
