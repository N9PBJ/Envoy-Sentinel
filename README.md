# Enphase DR Listener

Enphase DR Listener is a small Go service that watches an Enphase IQ Gateway on the local network and sends an email when it infers that a demand response event has started or ended.

The current MVP is local-only. It does not use the Enphase cloud Monitoring API or OAuth flow. Instead, it polls the IQ Gateway local API over HTTPS with a manually supplied bearer token.

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

The local API does not currently expose a documented `DR event active` field, so the app uses a heuristic detector.

## Detection Heuristic

The detector uses a small state machine:

```text
inactive -> suspected_active -> active -> suspected_ended -> inactive
```

Default behavior:

- Suspects an event after 3 consecutive polls where the battery is discharging at or above `1000 W`, SOC is above reserve, and there is export/surplus evidence.
- Export/surplus evidence means either grid export is at least `500 W`, or battery discharge exceeds the house's net demand after PV by at least `750 W`.
- Confirms an event if SOC drops by at least `2%` within 20 minutes while discharge remains sustained.
- Suspects an event ended when discharge falls below `300 W` for 10 minutes, or SOC reaches the configured reserve margin.
- Confirms the event ended after 15 minutes without sustained discharge.

This avoids treating normal battery self-consumption, such as a large AC load, as a DR event. The thresholds are intentionally conservative starting points and should be tuned after observing real GVEC/Enphase event data.

## Requirements

- Go 1.26 or newer
- Machine running on the same LAN as the Enphase IQ Gateway
- Enphase IQ Gateway owner token
- SMTP account for notifications

The gateway commonly uses a self-signed TLS certificate. The app allows that by default.

## Configuration

Required environment variables:

```powershell
$env:ENPHASE_GATEWAY_TOKEN = 'your-iq-gateway-token'
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
$env:DRLISTENER_POLL_INTERVAL = '60s'
$env:DRLISTENER_STATE_FILE = 'drlistener-state.json'
$env:ENPHASE_INSECURE_TLS = 'true'
```

Command-line flags can also be used:

```text
-gateway-url     IQ Gateway base URL, default https://envoy.local
-reserve-soc     configured battery reserve SOC percentage
-poll-interval   poll interval, default 60s
-insecure-tls    allow self-signed gateway TLS certificate, default true
-state-file      persistent detector state file, default drlistener-state.json
-test-email      send one SMTP test email and exit
-smtp-host       SMTP host
-smtp-port       SMTP port, default 587
-smtp-user       SMTP username
-smtp-pass       SMTP password
-smtp-from       sender email address
-smtp-to         recipient email address
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
export ENPHASE_GATEWAY_TOKEN='your-iq-gateway-token'
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

- Treat `ENPHASE_GATEWAY_TOKEN` like a password.
- Do not commit tokens, SMTP credentials, or the state file.
- The app permits self-signed gateway TLS by default because that is common for the local IQ Gateway API.
- Set `-insecure-tls=false` only if your gateway certificate can be verified by the host.


## TODO:
- Grid outage detection
- Automatic Token refresh