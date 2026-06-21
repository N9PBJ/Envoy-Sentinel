# Enphase DR Listener

Enphase DR Listener is a small Go service that watches an Enphase IQ Gateway on the local network and sends an email when it infers that a demand response event has started or ended.

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
- Main grid relay state for islanding detection

After two consecutive `main_relay_state = 2` readings, the tray icon flashes between the current battery level and a red outage icon. Grid restoration likewise requires two consecutive `main_relay_state = 1` readings; transitional state `3` preserves the last confirmed state.

The local API does not currently expose a documented `DR event active` field, so the app uses a heuristic detector.

## How the Pieces Fit Together

On startup, `main.go` loads configuration, obtains an Enphase bearer token, restores the detector snapshot, and starts the tray UI. A polling goroutine then repeats this flow:

1. Fetch and normalize `/ivp/livedata/status` into a gateway sample.
2. Pass the sample through the DR-event and grid-outage detectors.
3. Persist the DR detector snapshot before sending transition notifications.
4. Publish a locked status snapshot for the independently running tray updater.

The gateway client refreshes its bearer token and retries once after an HTTP 401. The token and account credentials remain in memory and are never intentionally logged or persisted.

## Detection Heuristic

The detector uses a small state machine:

```text
inactive -> suspected_active -> active -> suspected_ended -> inactive
```

Default behavior:

- Suspects an event after 3 consecutive polls where the battery is discharging at or above `1000 W`, SOC is above reserve, and there is export/surplus evidence.
- Export/surplus evidence means either grid export is at least `500 W`, or battery discharge exceeds the house's net demand after PV by at least `750 W`.
- Confirms an event if SOC drops by at least `2%` within 20 minutes while discharge remains sustained.
- Above reserve, suspects an event ended when discharge remains below `300 W` for 10 minutes. At reserve, idle/no-discharge is ambiguous because DR can suppress recharging; the detector remains active until charging at `300 W` or more resumes for 10 minutes.
- Confirms the event ended after 15 minutes without sustained discharge.

This avoids treating normal battery self-consumption, such as a large AC load, as a DR event. The thresholds are intentionally conservative starting points and should be tuned after observing real GVEC/Enphase event data.

## Requirements

- Go 1.26 or newer
- Machine running on the same LAN as the Enphase IQ Gateway
- Enphase system-owner account
- SMTP account for notifications

The gateway commonly uses a self-signed TLS certificate. The app allows that by default.

## Configuration

Required environment variables:

```powershell
$env:ENPHASE_USERNAME = 'owner@example.com'
$env:ENPHASE_PASSWORD = 'your-enphase-password'
$env:ENPHASE_GATEWAY_SERIAL = 'your-gateway-serial-number'
$env:ENPHASE_RESERVE_SOC = '20'
$env:SMTP_HOST = 'smtp.example.com'
$env:SMTP_FROM = 'drlistener@example.com'
$env:SMTP_TO = 'you@example.com'
```

Usually required for authenticated SMTP:

```powershell
$env:SMTP_PORT = '587'
$env:SMTP_USER = 'smtp-user'
$env:SMTP_PASS = 'smtp-password'
```

Optional environment variables:

```powershell
$env:ENPHASE_GATEWAY_URL = 'https://envoy.local'
$env:DRLISTENER_POLL_INTERVAL = '30s'
$env:DRLISTENER_STATE_FILE = 'drlistener-state.json'
$env:ENPHASE_INSECURE_TLS = 'true'
```

Command-line flags can also be used:

```text
-gateway-url     IQ Gateway base URL, default https://envoy.local
-reserve-soc     configured battery reserve SOC percentage
-poll-interval   poll interval, default 30s
-insecure-tls    allow self-signed gateway TLS certificate, default true
-state-file      persistent detector state file, default drlistener-state.json
-test-email      send one SMTP test email and exit
-smtp-host       SMTP host
-smtp-port       SMTP port, default 587
-smtp-user       SMTP username
-smtp-pass       SMTP password
-smtp-from       sender email address
-smtp-to         recipient email address
-log-file        combined text log, default envoy.log
-debug           save auxiliary gateway API responses under debug/
```

## Run

From PowerShell:

```powershell
go run . -gateway-url https://envoy.local
```

If `go` is not on PATH but installed in the default Windows location:

```powershell
& 'C:\Program Files\Go\bin\go.exe' run . -gateway-url https://envoy.local
```

To send a test email and exit:

```powershell
go run . -test-email
```

To build:

```powershell
go build -o drlistener.exe .
```

Then run:

```powershell
.\drlistener.exe -gateway-url https://envoy.local
```

## Linux Service Notes

Build on the target machine:

```bash
go build -o drlistener .
```

Run with environment variables:

```bash
export ENPHASE_USERNAME='owner@example.com'
export ENPHASE_PASSWORD='your-enphase-password'
export ENPHASE_GATEWAY_SERIAL='your-gateway-serial-number'
export ENPHASE_RESERVE_SOC='20'
export SMTP_HOST='smtp.example.com'
export SMTP_PORT='587'
export SMTP_USER='smtp-user'
export SMTP_PASS='smtp-password'
export SMTP_FROM='drlistener@example.com'
export SMTP_TO='you@example.com'

./drlistener -gateway-url https://envoy.local
```

For a long-running Linux install, put the environment variables in a protected environment file and run the binary under systemd.

## State File

The app writes a small JSON state file, default:

```text
drlistener-state.json
```

This prevents duplicate start/end notifications if the process restarts during an inferred event. Keep this file writable by the service user and avoid committing it.

## Logging and Diagnostics

The app uses Go's structured `slog` logger. Normal operation and state transitions are `INFO`, a confirmed grid outage is `WARN`, and failed operations are `ERROR`. Logs go to both standard output and the file selected by `-log-file`/`LOGFILE`; source locations are included.

Debug mode calls several auxiliary gateway endpoints and saves their raw responses under `debug/` for offline investigation. Those calls are best-effort and do not stop normal live-data polling if one fails. Debug files may contain system telemetry, so review them before sharing.

## Tests

Run:

```powershell
go test ./...
```

If Go's default build cache is not writable in the current shell, use a local cache:

```powershell
$env:GOCACHE = 'D:\DRListener\.gocache'
go test ./...
```

## Security Notes

- Protect `ENPHASE_USERNAME` and `ENPHASE_PASSWORD`; the app uses them to request a token but does not persist or log the token.
- Do not commit Enphase credentials, SMTP credentials, or the state file.
- The app permits self-signed gateway TLS by default because that is common for the local IQ Gateway API.
- Set `-insecure-tls=false` only if your gateway certificate can be verified by the host.
