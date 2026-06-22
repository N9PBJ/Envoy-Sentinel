# Envoy Sentinel

Envoy Sentinel is a small Go application that watches an Enphase IQ Gateway on the local network and can send an email when it infers that a demand response event has started or ended.

The app polls the IQ Gateway local API over HTTPS. At startup it uses the system owner's Enphase credentials and the configured gateway serial number to obtain the required bearer token automatically.

## What It Monitors

The app polls:

```text
GET https://envoy.local/ivp/livedata/status
```

It normalizes the local gateway response into:

- Battery state of charge
- Battery charge/discharge power
- Grid import/export power
- Solar production power
- Load power
- Demand-response diagnostic counters, when present
- Main grid relay state for outage and manual-disconnect detection

After two consecutive `main_relay_state = 0` readings, the tray icon flashes between the current battery level and a red outage icon. State `2` is shown as a manual grid disconnect without an outage alert. Grid restoration likewise requires two consecutive `main_relay_state = 1` readings; transitional state `3` preserves the last confirmed state.

The local API does not currently expose a documented `DR event active` field, so the app uses a heuristic detector.

## How the Pieces Fit Together

On startup, `main.go` loads configuration, obtains an Enphase bearer token,
restores the detector snapshot, performs an initial poll, and starts the tray
and live-status UI. A polling goroutine then repeats this flow:

1. Fetch and normalize `/ivp/livedata/status` into a gateway sample.
2. Pass the sample through the DR-event and grid-outage detectors.
3. Persist the DR detector snapshot and any pending transition notification.
4. Publish one locked status snapshot for the independently running tray and
   live-status renderers.
5. Deliver and acknowledge any pending transition notification.

## Live Status Window

Envoy Sentinel opens a native live-status window at startup. Open it again at
any time from **Open Live Status** in the tray menu. Closing the window hides it
without stopping monitoring; opening it again starts a new 15-minute viewing
session. Like the Enphase live view, the window automatically hides when that
countdown expires. The countdown controls the viewing window only: background
gateway polling, detection, persistence, tray updates, and notifications keep
running.

The diagram displays solar production, house consumption, grid import or
export, battery charging or discharging, battery charge, profile/DR status, and
grid connection state. Moving dots show the direction of active power flow.
The displayed **Demand Response** profile is the detector's inferred state;
the local gateway API does not provide a documented official profile name.

The **Controller polling** selector below the diagram shows the active polling
interval and can change it without restarting the app. Presets are 5, 10, 15,
or 30 seconds and 1, 2, or 5 minutes. A different configured startup value is
also included in the list. Selecting a value resets the ticker, so the next
poll occurs after one complete interval rather than immediately. This is a
runtime override only; the next launch starts with
`DRLISTENER_POLL_INTERVAL` from `.env` (or 30 seconds when it is unset).

The gateway client refreshes its bearer token and retries once after an HTTP 401. The token and account credentials remain in memory and are never intentionally logged or persisted.

When SMTP notifications are enabled, transitions use a small persistent outbox: a start or end transition remains pending until delivery succeeds. This favors eventual delivery over strict deduplication; a crash after SMTP accepts a message but before its acknowledgement is saved can produce a duplicate rather than silently losing the event.

## Detection Heuristic

The detector uses a small state machine:

```text
inactive -> suspected_active -> active -> suspected_ended -> inactive
```

Default behavior:

- Suspects an event after at least 30 continuous seconds of battery discharge above the house's solar deficit and at least `150 Wh` of that excess energy accumulates within 10 minutes. This is independent of the selected polling interval.
- Excess dispatch is battery discharge minus the portion needed to cover house consumption after PV. Long periods of ordinary self-consumption therefore contribute no evidence, while short solar-control overshoots contribute too little energy.
- Confirms an event if SOC drops by at least `2%` within 20 minutes while discharge remains sustained.
- Above reserve, suspects an event ended when discharge remains below `300 W` for 10 minutes. At reserve, idle/no-discharge is ambiguous because DR can suppress recharging; the detector remains active until charging at `300 W` or more resumes for 10 minutes.
- Confirms the event ended after 15 minutes without sustained discharge.

This avoids treating normal battery self-consumption, such as a large AC load, as a DR event. The thresholds are intentionally conservative starting points and should be tuned after observing real GVEC/Enphase event data.

## Requirements

- Windows 10 or newer on an amd64 or arm64 machine
- Machine running on the same LAN as the Enphase IQ Gateway
- Enphase system-owner account
- SMTP account if email notifications are enabled

Go 1.26 or newer is required only when running from source. The gateway commonly uses a self-signed TLS certificate, which the app allows by default.

## Configuration

Required environment variables:

```powershell
$env:ENPHASE_USERNAME = 'owner@example.com'
$env:ENPHASE_PASSWORD = 'your-enphase-password'
$env:ENPHASE_GATEWAY_SERIAL = 'your-gateway-serial-number'
$env:ENPHASE_RESERVE_SOC = '20'
```

These values may be set directly in the process environment or placed in an optional `.env` file beside the executable.

SMTP notifications are disabled by default. To enable them, configure:

```powershell
$env:SMTP_NOTIFICATIONS_ENABLED = 'true'
$env:SMTP_HOST = 'smtp.example.com'
$env:SMTP_PORT = '587'
$env:SMTP_USER = 'smtp-user'
$env:SMTP_PASS = 'smtp-password'
$env:SMTP_FROM = 'envoy-sentinel@example.com'
$env:SMTP_TO = 'you@example.com'
```

`SMTP_USER` and `SMTP_PASS` may be omitted when the SMTP server does not require authentication. When notifications are disabled, no SMTP settings are required and the tray's test-email item is disabled.

Optional environment variables:

```powershell
$env:ENPHASE_GATEWAY_URL = 'https://envoy.local'
$env:DRLISTENER_POLL_INTERVAL = '30s' # startup interval; the UI can override it until restart
$env:DRLISTENER_STATE_FILE = 'drlistener-state.json'
$env:ENPHASE_INSECURE_TLS = 'true'
```

Command-line flags can also be used:

```text
-gateway-url     IQ Gateway base URL, default https://envoy.local
-reserve-soc     configured battery reserve SOC percentage
-poll-interval   startup poll interval, default 30s
-insecure-tls    allow self-signed gateway TLS certificate, default true
-state-file      persistent detector state file, default drlistener-state.json
-smtp-notifications
                  send DR transition emails, default false
-smtp-host       SMTP host
-smtp-port       SMTP port, default 587
-smtp-user       SMTP username
-smtp-pass       SMTP password
-smtp-from       sender email address
-smtp-to         recipient email address
-log-file        combined text log, default envoy.log
-debug           enable DEBUG log records, including per-poll telemetry
-dump-api-responses
                 save raw gateway API responses under debug/
```

## Run a Precompiled Release

Precompiled Windows releases are available on the [GitHub Releases page](https://github.com/N9PBJ/Envoy-Sentinel/releases). Choose the ZIP matching your machine:

- `windows_amd64` for most Intel and AMD Windows computers
- `windows_arm64` for Windows on ARM

Download and extract the archive, then place your `.env` file beside `envoy-sentinel.exe` or set the configuration in the process environment. From PowerShell:

```powershell
Expand-Archive .\envoy-sentinel_VERSION_windows_amd64.zip -DestinationPath .\envoy-sentinel
Set-Location .\envoy-sentinel
.\envoy-sentinel.exe
```

The release archive contains only `envoy-sentinel.exe`. Tray icons, the native
live-status UI, and the Windows Common Controls/DPI manifest are embedded in
the executable. The state file, log, and optional `debug/` directory are
created at runtime; credentials and `.env` are deliberately never included in
a release.

Published releases also include `checksums.txt`, which can be used to verify the downloaded archive:

```powershell
Get-FileHash .\envoy-sentinel_VERSION_windows_amd64.zip -Algorithm SHA256
```

Compare the resulting hash with the corresponding entry in `checksums.txt`.

## Run from Source

Install Go 1.26 or newer, clone the repository, and run it from PowerShell:


```powershell
git clone https://github.com/N9PBJ/Envoy-Sentinel.git
Set-Location .\Envoy-Sentinel
go run .
```

If `go` is not on PATH but installed in the default Windows location:

```powershell
& 'C:\Program Files\Go\bin\go.exe' run .
```

When SMTP notifications are enabled, test them from the tray menu by clicking **Send Test Email...**. Click the confirmation item within 10 seconds to send; otherwise it cancels automatically. Delivery happens in the background so the tray and gateway polling remain responsive.

To build the executable yourself:

```powershell
go build -ldflags "-H=windowsgui" -o envoy-sentinel.exe .
.\envoy-sentinel.exe -gateway-url https://envoy.local
```

Architecture-specific `rsrc_windows_*.syso` files embed the application icon
and Windows manifest automatically during `go build`; no separate resource
compiler or C toolchain is required.

## State File

The app writes a small JSON state file, default:

```text
drlistener-state.json
```

This restores active and provisional detector timing after a restart and retains undelivered notifications for retry. Keep this file writable by the service user and avoid committing it. Updates use a flushed temporary file followed by an atomic replacement so an interrupted write does not leave partial JSON.

## Logging and Diagnostics

The app uses Go's structured `slog` logger. Normal operation and state transitions are `INFO`, a confirmed grid outage is `WARN`, and failed operations are `ERROR`. Logs go to both standard output and the file selected by `-log-file`/`LOGFILE`; source locations are included. Passing `-debug` enables `DEBUG` records, including per-poll telemetry samples, without saving raw API responses. Debug logging can also be enabled with `DRLISTENER_DEBUG=true`.

Passing `-dump-api-responses` calls several auxiliary gateway endpoints on
every poll and saves their raw responses under `debug/` for offline
investigation. It can also be enabled with
`DRLISTENER_DUMP_API_RESPONSES=true`. Those calls are
best-effort and do not stop normal live-data polling if one fails. Short
runtime polling intervals therefore produce substantially more gateway traffic
and response files. These files may contain system telemetry, so review them
before sharing.

## Tests

Run:

```powershell
go test ./...
```

Run the same static checks used during development:

```powershell
go vet ./...
golangci-lint run ./...
```

If Go's default build cache is not writable in the current shell, use a local cache:

```powershell
$env:GOCACHE = Join-Path $PWD '.gocache'
go test ./...
```

## Security Notes

- Protect `ENPHASE_USERNAME` and `ENPHASE_PASSWORD`; the app uses them to request a token but does not persist or log the token.
- Do not commit Enphase credentials, SMTP credentials, or the state file.
- The app permits self-signed gateway TLS by default because that is common for the local IQ Gateway API.
- Set `-insecure-tls=false` only if your gateway certificate can be verified by the host.
