# FranklinWH Cloud API (unofficial)

This document describes the HTTP API used by the **FranklinWH** mobile app
(`com.Franklinwh.FamilyEnergy`) to monitor and control aGate / aPower home
energy systems. It was reverse engineered from **version 2.23.0** of the
Android app so that homeowners can read their own battery, grid and solar
status from their own scripts.

> **Unofficial and unsupported.** FranklinWH does not publish or support this
> API. Field names, endpoints and behavior can change without notice. Use your
> own account and credentials, and be gentle with the servers — the app polls
> live data only about every 10 seconds.

## Contents

- [How the app is built](#how-the-app-is-built)
- [Hosts and environments](#hosts-and-environments)
- [Request format and headers](#request-format-and-headers)
- [Response envelope](#response-envelope)
- [Authentication](#authentication)
  - [Password encryption](#password-encryption)
  - [Login](#login)
  - [Multi-factor authentication](#multi-factor-authentication)
  - [CAPTCHA (pre-login)](#captcha-pre-login)
- [Reading status](#reading-status)
  - [List gateways](#list-gateways)
  - [Device composite info (live telemetry)](#device-composite-info-live-telemetry)
  - [`runtimeData` field glossary](#runtimedata-field-glossary)
  - [Other useful read endpoints](#other-useful-read-endpoints)
- [Grid import/export limits](#grid-importexport-limits)
- [Device passthrough: `sendMqtt`](#device-passthrough-sendmqtt)
- [Enumerations](#enumerations)
- [Direct / local connections](#direct--local-connections)
- [Endpoint catalog](#endpoint-catalog)
- [Provenance](#provenance)

## How the app is built

The app is a **Flutter** application (Dart 3.11 AOT). Networking is done with
the Dart `dio` package against a REST-ish JSON API. Real-time device data is
served two ways:

1. **Through the cloud** (what this document focuses on): the app calls
   `getDeviceCompositeInfo`, and for lower-level data it POSTs a framed
   command to `sendMqtt`, which the cloud relays to the gateway over AWS IoT
   MQTT and returns the reply inline.
2. **Directly** over the local network (TCP socket) or **Bluetooth**, used
   mostly during installation/commissioning. See
   [Direct / local connections](#direct--local-connections).

The app also opens its **own** AWS IoT MQTT connection (via Amazon Cognito)
for push updates, but all the data below is reachable over plain HTTPS.

## Hosts and environments

| Environment | Base URL |
|---|---|
| **Production** | `https://energy.franklinwh.com` |
| Production (alt) | `https://energy.franklinwh.solar` |
| UAT | `https://uat.franklinwh.com` |
| Test | `https://test.franklinwh.com` |
| Dev | `https://dev.franklinwh.com` |

The main service is mounted under the path prefix **`/hes-gateway`**. Other
services seen in the app include `/api-user` (MFA), `/api-energy` (historical
statistics), `/hes-tariff-collection`, `/hes-upgrade`, and `/api-vpp-portal`.

Within `/hes-gateway`, endpoints are grouped by role:

- **`/terminal/...`** — homeowner endpoints (use these).
- **`/manage/...`** — installer endpoints (same shapes, installer scope).
- **`/common/...`** — shared.

## Request format and headers

All calls are HTTPS. `GET` requests pass parameters in the query string;
`POST` requests send a JSON body (`Content-Type: application/json`) unless
noted. The app sends these headers on **every** request:

| Header | Example | Meaning |
|---|---|---|
| `loginToken` | `e30b…` | Session token from login. Omitted before login. |
| `lang` | `EN_US` | UI language (`EN_US`, `ZH_CN`, …). |
| `softwareVersion` | `APP2.23.0` | Literal `APP` + app version. |
| `optTime` | `2026-10-02 14:30:00` | Client time, `yyyy-MM-dd HH:mm:ss`. |
| `optSource` | `3` | Constant; `3` = Android app. |
| `optDevice` | `Pixel 7` | Device model (informational). |
| `optDeviceName` | `My Phone` | Device name (informational). |
| `optSystemVersion` | `Android 14` | OS version (informational). |

The `opt*` headers appear to be recorded but not validated. The token header
is the only one required for authenticated calls.

## Response envelope

Every endpoint returns a JSON envelope:

```json
{
  "code": 200,
  "message": "Query success!",
  "result": { "...": "..." },
  "total": 1,
  "success": true
}
```

- **`code`** — `200` on success. `401` means the token is missing/expired
  (the app logs the user out). Some older responses use `0` for success and
  put the payload under `data` instead of `result`.
- **`result`** — the payload: an object, an array, or a scalar.
- **`success`** — boolean mirror of success.

On an expired token the app receives **HTTP 401** with body `code: 401`
("invalid token!") and forces a re-login.

## Authentication

### Password encryption

> **Note:** a plain lowercase hex MD5 of the password, sent **without** an
> `enc` field, is what the server reliably accepts. The AES scheme below was
> rejected with "Incorrect password" for a real homeowner account, so the
> client only uses it when `LoginOptions.EncryptPassword` is set.

The app does not send the password in cleartext (beyond TLS). It obfuscates
it client-side; the server expects this exact transformation:

1. `hash = lowercase_hex(MD5(password))` — a 32-character string.
2. `key  = middle 16 characters of hash` — i.e. `hash[8:24]`.
3. `ciphertext = AES-128-CBC(key, random 16-byte IV, PKCS7(hash))`
   — note the **plaintext is the MD5 hash string**, not the password.
4. Send `base64(ciphertext) + ":" + base64(iv)` as the `password` field,
   together with `enc: "1"`.

This is not meaningful cryptography (the scheme is public and the key is
derived from the hash itself); it only matches what the server validates. A
reference implementation is in [`auth.go`](../auth.go) (`encryptPassword`).

### Login

```
POST /hes-gateway/terminal/initialize/appUserOrInstallerLogin
Content-Type: application/x-www-form-urlencoded
client-id: <stable UUID for this install>
```

Body (form fields, shown one per line):

```
account=you@example.com
password=<lowercase hex MD5 of the password>
type=0
lang=en_US
softwareVersion=APP2.23.0
optDevice=Pixel 7
optSystemVersion=Android 14
userType=
captchaId=
captchaCode=
timezone=America/Chicago
```

The body **must be form-encoded**. A JSON body is not bound: the server
sees an empty `account` and fails with `code: 500` ("Unexpected runtime
error occurred ... judgeAccountExist"), whatever the credentials.

- **`type`** is the account type: `0` = homeowner, `2`/`3` = installer
  (see [Account types](#account-types)).
- **`client-id`** is a stable per-install identifier (the app uses a UUID). It
  doubles as the MFA "device fingerprint", so keep it constant to benefit from
  "remember this device".

Success `result` (abridged):

```json
{
  "token": "…",
  "userId": 1205,
  "loginName": "you@example.com",
  "firstLogin": false,
  "countryId": 2,
  "mfaRequired": false
}
```

Use `token` as the `loginToken` header on all later calls.

### Multi-factor authentication

If the account has MFA enabled, the login `result` instead contains:

```json
{
  "mfaRequired": true,
  "mfaToken": "…",
  "mfaMethod": "EMAIL_OTP",
  "availableMfaMethods": ["EMAIL_OTP", "TOTP"],
  "maskedEmail": "y***@example.com",
  "lockSeconds": 0
}
```

Methods: **`TOTP`** (authenticator app) and **`EMAIL_OTP`** (code emailed).

For `EMAIL_OTP`, request the email first:

```
POST /hes-gateway/terminal/initialize/mfa/email-otp/login/send
{ "mfaToken": "…" }
```

Then verify (both methods):

```
POST /hes-gateway/terminal/initialize/mfa/login/verify
{
  "mfaToken": "…",
  "code": "123456",
  "method": "EMAIL_OTP",
  "deviceFingerprint": "<same client-id>",
  "rememberDevice": true
}
```

The verify `result` returns the same shape as a normal login, including the
`token`. (Installer accounts use the `/api-user/mfa/...` host for the same
operations; homeowners use the `/terminal/initialize/mfa/...` paths above.)

### CAPTCHA (pre-login)

The app can pre-check an account before login:

```
POST /hes-gateway/common/preLogin
Content-Type: application/x-www-form-urlencoded
{ "loginName": "you@example.com" }
```

If the `result` has `needCaptcha: true`, it also returns `captchaId` and
`captchaImage`; the answer goes back in the login `captchaId`/`captchaCode`
fields. In practice normal logins do not require a CAPTCHA.

## Reading status

### List gateways

```
GET /hes-gateway/terminal/getHomeGatewayList
```

Returns an array of the gateways on the account. Each has an **`id`** (the
`gatewayId` used by every other call), `name`, `protocolVer`, `version`,
`zoneInfo`, etc. (Demo accounts use `/terminal/listAppUserDevice`; installers
use `/manage/selectAppGatewayList` with paging/filter parameters.)

### Device composite info (live telemetry)

This is the main status call — it backs the app's home screen.

```
GET /hes-gateway/terminal/getDeviceCompositeInfo?gatewayId=<ID>&refreshFlag=1
```

- **`refreshFlag`** — `1` asks the cloud to pull fresh data from the gateway;
  `0` returns the last cached values.

Polled too often (or from several clients on one account at once), the server
answers HTTP 200 with `{"code":429,"message":"Too Many Requests"}`. Back off
and retry; the library reports it as `ErrRateLimited`.

`result`:

```json
{
  "currentWorkMode": 3,
  "deviceStatus": 1,
  "runtimeData": { "...": "see below" },
  "solarHaveVo": { "offGridFlag": 0, "remoteSolarEn": 0, "...": "..." },
  "currentAlarmVOList": [],
  "valid": true
}
```

- **`currentWorkMode`** — operating mode, see [DeviceMode](#device-mode).
- **`deviceStatus`** — overall status, see [DeviceStatus](#device-status).
- **`solarHaveVo.offGridFlag`** — `1` when the system is running off-grid.

### `runtimeData` field glossary

Instantaneous power is in **kW** (cloud responses are already scaled). Energy
counters are in **kWh**. Signs below are confirmed against real data.

| Field | Type | Meaning |
|---|---|---|
| `soc` | float | **Battery state of charge, %** (whole system). |
| `p_fhp` | float | **Battery power, kW.** `< 0` charging, `> 0` discharging. |
| `p_uti` | float | **Grid power, kW.** `> 0` importing, `< 0` exporting. |
| `p_sun` | float | **Solar power, kW** (production). |
| `p_gen` | float | **Generator power, kW** (production). |
| `p_load` | float | **Home load, kW** (consumption). |
| `fhpSn` | [string] | Per-aPower serial numbers. |
| `fhpSoc` | [float] | Per-aPower SoC, %. |
| `fhpPower` | [float] | Per-aPower power, kW (`< 0` charging). |
| `name` | string | Human-readable mode, e.g. `"Time of Use"`. |
| `mode` | int | Internal mode bitfield/code. |
| `run_status` | int | See [RunStatus](#run-status): 0 standby, 1 charging, 2 discharging, 3 error. |
| `electricity_type` | int | Grid service type. |
| `elecnet_state` | int | Grid/network state; `0` = on-grid. |
| `kwh_uti_in` | float | Lifetime energy imported from grid, kWh. |
| `kwh_uti_out` | float | Lifetime energy exported to grid, kWh. |
| `kwh_sun` | float | Lifetime solar production, kWh. |
| `kwh_gen` | float | Lifetime generator production, kWh. |
| `kwh_fhp_chg` | float | Lifetime battery charge, kWh. |
| `kwh_fhp_di` | float | Lifetime battery discharge, kWh. |
| `kwh_load` | float | Lifetime home consumption, kWh. |
| `t_amb` | float | Ambient temperature. |
| `signal` | int | Cellular/Wi-Fi signal strength. |
| `connType` | int | Connection type (see [NetworkType](#network-type)). |
| `main_sw` | [int] | Main switch state per phase. |
| `pro_load` | [int] | Protected-load switch state. |
| `genStat` | int | Generator status (see [GeneratorStatus](#generator-status)). |
| `solarPower`, `remoteSolar1Power`, `remoteSolar2Power` | int | Solar sub-readings (W) on some installs. |

Example (grid importing 0.655 kW, battery nearly full and trickle-charging):

```json
{
  "name": "Emergency Backup", "soc": 99.158,
  "p_uti": 0.655, "p_sun": -0.007, "p_gen": 0.0, "p_fhp": -0.163, "p_load": 0.485,
  "fhpSoc": [99.1, 99.1, 99.4], "fhpPower": [-0.055, -0.049, -0.06],
  "kwh_uti_in": 8.031, "kwh_load": 7.378, "elecnet_state": 0
}
```

### Other useful read endpoints

All take `?gatewayId=<ID>` unless noted.

| Endpoint | Returns |
|---|---|
| `GET /hes-gateway/terminal/getDeviceInfoV2` | Static device info: firmware, timezone, battery count, `fhpSn`, country/province. |
| `GET /hes-gateway/terminal/obtainApowersInfo` | Per-aPower detail: `ratedPower`, `ratedCapacity`, `remainingPower`, `soc`, firmware versions. |
| `GET /hes-gateway/terminal/selectDeviceOverallInfo` | `apowerCount`, `totalPower` (total capacity kWh). |
| `GET /hes-gateway/terminal/obtainAgateInfo` | aGate firmware/version block. |
| `GET /hes-gateway/terminal/selectOffgrid?gatewayId=&type=0` | `offgridSet`, `offgridState`. |
| `GET /hes-gateway/terminal/chargePowerDetails` | Battery runtime estimates. |
| `GET /hes-gateway/terminal/backupHistorySummary` | Backup event history summary. |
| `GET /api-energy/power/getFhpPowerByDay?gatewayId=&dayTime=YYYY-MM-DD` | Power time-series arrays for a day (home screen graph). |
| `GET /api-energy/electric/getFhpElectricData?gatewayId=&type=&startDate=` | Energy statistics by day/week/month/year (`type` selects period). |

## Grid import/export limits

The app's "Power Control System" screen. Newer app versions (seen in
2.20.1) show these as read-only for homeowners ("Setting may not be changed.
Contact your installer if required."); the API still returns them.

```
GET /hes-gateway/terminal/tou/getPowerControlSetting?gatewayId=<ID>
```

Live `result` from a homeowner account (abridged):

```json
{
  "gridMax": 2.5,               "gridMaxFlag": 2,
  "gridFeedMax": 2.0,           "gridFeedMaxFlag": 2,
  "globalGridChargeMax": 2.5,   "globalGridDischargeMax": 2.0,
  "globalSettingStatus": 0,
  "gridFlag": true, "solarFlag": true,
  "notControlExportSolar": false,
  "sgipFlag": 1, "itcFlag": 1, "isNem3": 0, "isCalifornia": 1,
  "peakDemandGridMax": null, "apowerNumber": 1
}
```

- **`globalGridChargeMax`** — stored grid **import** limit, kW (what the app shows).
- **`globalGridDischargeMax`** — stored grid **export** limit, kW.
- **`gridMax` / `gridFeedMax`** — normally equal to the two above. They were
  seen at `-1` / `10` (apparently "no limit" / the maximum) after some writes.
- **`gridMaxFlag` / `gridFeedMaxFlag`** — limit mode. `2` accompanies a kW
  limit ("Limit"). The app's other options are "No limit" and "No Export";
  their codes are **unconfirmed**.

To change them:

```
POST /hes-gateway/terminal/tou/setPowerControlV2
Content-Type: application/json
softwareVersion: APP2.6.2
```

Body: the `getPowerControlSetting` result with `gatewayId` added. Verified
against a live gateway:

- The server takes the new limits from **`gridMax`** (import) and
  **`gridFeedMax`** (export) and stores them into `globalGridChargeMax` /
  `globalGridDischargeMax`. The `global*` fields in the request are ignored,
  so sending a read result back unchanged can *change* the limits if
  `gridMax`/`gridFeedMax` differ from the `global*` values.
- For a homeowner, the server answers `code: 400` "This setting is read-only.
  Please contact your Certified Installer…" when the `softwareVersion` header
  is a current app version (2.23.0). With an older version (`APP2.6.2`) it
  accepts the change. The older `/terminal/tou/setPowerControl` behaves the
  same way.

## Device passthrough: `sendMqtt`

For data not in `getDeviceCompositeInfo`, the app POSTs a framed command that
the cloud relays to the gateway (over MQTT) and answers inline.

```
POST /hes-gateway/terminal/sendMqtt        (installers: /manage/sendMqtt)
Content-Type: application/json
```

Request frame:

```json
{
  "cmdType": 211,
  "equipNo": "<gatewayId>",
  "type": 0,
  "timeStamp": 1775713122,
  "snno": 0,
  "len": 41,
  "crc": "6670AEC5",
  "dataArea": { "fhpSn": "<aPowerSn>", "type": 2 }
}
```

Framing rules (verified against captured data):

- **`timeStamp`** — current Unix time (seconds).
- **`len`** — byte length of `dataArea` serialized as **compact JSON**
  (`{"key":value,...}`, no spaces).
- **`crc`** — **CRC-32** (IEEE, same polynomial as zlib) of that compact JSON,
  as **8 uppercase hex digits**.
- **`snno`** — sequence number; `0` is accepted on request.
- The reply uses `cmdType + 1` and carries `dataArea` either as a nested
  object or as a JSON **string** that must be parsed again.

Command types (`cmdType`) relevant to monitoring — the reply is `cmdType + 1`:

| `cmdType` | Name | `dataArea` request | Reply contains |
|---|---|---|---|
| `211` | aPower / BMS detail | `{"fhpSn":"<sn>","type":2}` or `{"type":1}` | Cell voltages/temps, `batSoc`, `batCurr`, inverter voltages, run modes. With `type:1`: gateway-level grid voltages, relays (`gridRelayStat`, `solarRelayStat`, …). |
| `317` | Network/comm settings | `{"optType":0,"paraType":6}` | `commSetPara`: Wi-Fi/eth/cellular MACs, IPs, DNS, `awsStatus`. |
| `327` | aPower LED | `{"opt":0}` | `lightStat[]`, schedule. |
| `339` | Connection status | `{"opt":0}` | `routerStatus`, `netStatus`, `awsStatus`, signal strengths, `currentNetType`. |
| `341` | Network switches | `{"opt":0}` | Per-interface enable flags. |

The app maps higher-level reads to either an HTTP endpoint or an MQTT
`cmdType` depending on the connection mode. The full map (BLE/socket opcode vs
MQTT `cmdType`) is in the app's `CommunicationCmd` enum; monitoring-relevant
ones include `queryDeviceInfo` (MQTT 503), `deviceDetailInfo` (203),
`solarInfo` (325), `generatorInfo` (323), and `deviceData`/aPower (211).

For most monitoring you only need `getDeviceCompositeInfo`; `sendMqtt` is for
cell-level battery data and diagnostics.

## Enumerations

### Account types
`0` homeowner · `2` distributor · `3` installer · (higher values are internal roles).

### Device mode
(`currentWorkMode`) — `0` unknown · `1` Time of Use · `2` Self Consumption ·
`3` Emergency Backup · `4` Battery Bonus · `6` Solar + Battery Savings ·
`7` Smart Energy Dispatch.

### Device status
(`deviceStatus`) — `0` standby · `1` charging · `2` discharging · `3` fault · `4` offline.

### Run status
(`run_status`) — `0` standby · `1` charging · `2` discharging · `3` error.

### Generator status
(`genStat`) — `0` disable · `1` stop · `2` start · `3` running · `4` quit · `5` error · `6` exercise.

### aPower status
`0` normal · `1` warning · `2` fault · `3` offline · `4` off · `5` self-test.

### Network type
(`connType`) — `2` ethernet · `3` Wi-Fi · `4` cellular.

### EMS run status (detailed)
`0` initial · `1` standby · `2` self-consumption · `3` supply priority ·
`4` storage priority · `6` storm waiting · `7` off-grid battery backup ·
`8` off-grid generator backup · `9` VPP · `a` emergency stop · … (30 values;
see `EmsRunStatus` in the app).

## Direct / local connections

The app can also reach the gateway without the cloud, mainly for
installation:

- **TCP socket** to the gateway at **`10.100.1.1:18000`** (the gateway's own
  Wi-Fi AP / local network) using the same frame structure as `sendMqtt`
  (`cmdType`, `equipNo`, `len`, `crc`, `dataArea`). On the socket the frame is
  lightly obfuscated: the payload is `base64(cmdType),base64(compactJson)` with
  a per-byte additive transform keyed by a one-byte "secret" = `(sum of the
  gateway serial's UTF-8 bytes) & 0xFF`. This is obfuscation, not encryption.
- **Bluetooth LE** for onboarding (Wi-Fi provisioning, password setup).
- **SunSpec Modbus TCP** — the app exposes an installer setting to enable a
  SunSpec Modbus TCP server on the aGate, which would allow standard
  local Modbus monitoring tools to read the system. This is the most
  "standards-based" local option if you can enable it on your unit.

These are out of scope for the Go client here (which targets the cloud HTTPS
API), but are documented for completeness.

## Endpoint catalog

A fuller list of endpoints seen in the app is in
[`endpoints.txt`](endpoints.txt) (homeowner `/terminal/...`, installer
`/manage/...`, and others). Only a subset is needed for monitoring; the ones
above cover battery, grid and solar status.

## Provenance

- Source: APK `com.Franklinwh.FamilyEnergy` version **2.23.0** (versionCode 178).
- The app is Flutter; Dart logic was recovered from `libapp.so` (AOT snapshot)
  and the native plugins from the Java/Kotlin dex.
- Response shapes were cross-checked against the app's own bundled sample data
  (`asset/mock_data/*.json`, used for the in-app demo mode), and the request
  format was validated against the live server (which rejects a bad token with
  HTTP 401 / `code: 401` exactly as the client expects).
- Nothing here required a FranklinWH account to *discover*; use your own
  account to *use* it.
