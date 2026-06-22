package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"drlistener/internal/config"
	"drlistener/internal/detector"
	"drlistener/internal/gateway"
	"drlistener/internal/notify"
	"drlistener/internal/outage"
	"drlistener/internal/state"

	"github.com/getlantern/systray"
	"github.com/joho/godotenv"
)

const configFile = ".env"

var (
	// Status is the most recent normalized gateway reading. pollOnce publishes
	// it under statusMu; the tray and live-status renderers copy it under the
	// same lock so network polling never blocks on Windows UI work.
	Status   liveStatusSnapshot
	statusMu sync.RWMutex

	// The selector in the live-status window updates runtimePollInterval and
	// sends the newest value to the polling goroutine. The buffered channel is
	// deliberately latest-value-wins so the Windows UI thread never blocks.
	pollMu              sync.RWMutex
	runtimePollInterval time.Duration
	pollIntervalChanges = make(chan time.Duration, 1)
)

// liveStatusSnapshot is the immutable-by-convention value copied by each UI
// consumer. HasSample distinguishes a real all-zero reading from startup.
// LastError describes only the newest failed poll; a successful poll clears it.
type liveStatusSnapshot struct {
	SOC            float64
	State          detector.State
	BatteryPowerW  float64
	GridPowerW     float64
	PVPowerW       float64
	LoadPowerW     float64
	MainRelayState int
	GridState      outage.State
	GridOutage     bool
	UpdatedAt      time.Time
	HasSample      bool
	LastError      string
}

type emailSender interface {
	Send(subject, body string) error
}

func initLogging(logFilename string, debug bool) error {
	logfile, err := os.OpenFile(
		logFilename,
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0644,
	)
	if err != nil {
		return fmt.Errorf("error opening log file: %v", err)
	}

	// Keep console output for interactive runs while retaining a persistent log
	// for the desktop application, whose release build has no visible console.
	mw := io.MultiWriter(os.Stdout, logfile)
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	handler := slog.NewTextHandler(mw, &slog.HandlerOptions{
		AddSource: true,
		Level:     level,
	})
	slog.SetDefault(slog.New(handler))

	return nil
}

func fatal(message string, args ...any) {
	// slog deliberately has no Fatal method because logging and process control
	// are separate concerns. Startup failures are the one place we combine them.
	slog.Error(message, args...)
	os.Exit(1)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		slog.Info("shutdown requested")
		quitLiveStatusWindow()
		systray.Quit()
	}()

	// Read .env file if it exists
	err := godotenv.Load(configFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fatal("error loading env file", "error", err)
	}

	// Load the configuration
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		fatal("config error", "error", err)
	}

	// Initialize logging to stdout and a file
	err = initLogging(cfg.Logfile, cfg.Debug)
	if err != nil {
		fatal("initialize logging", "error", err)
	}

	// Initialize the emailer
	var emailer emailSender
	if cfg.SMTPNotifications {
		emailer = notify.NewEmailer(cfg.SMTP)
	}
	slog.Info("SMTP notifications configured", "enabled", cfg.SMTPNotifications)
	// Initialize the envoy client
	client, err := gateway.NewClient(cfg.GatewayURL, "", cfg.AllowInsecureTLS)
	if err != nil {
		fatal("gateway client", "error", err)
	}

	// Gateway bearer tokens expire. The client calls this provider once at
	// startup and retries a request with a fresh token after one HTTP 401.
	authenticator := gateway.NewCloudAuthenticator()
	client.SetTokenProvider(func(ctx context.Context) (string, error) {
		return authenticator.Token(ctx, cfg.EnphaseUsername, cfg.EnphasePassword, cfg.GatewaySerial)
	})
	if err := client.RefreshToken(ctx); err != nil {
		fatal("authenticate with Enphase", "error", err)
	}
	slog.Info("gateway access token acquired", "serial", cfg.GatewaySerial)

	snapshot, err := state.Load(cfg.StatePath)
	if err != nil {
		fatal("load state", "error", err)
	}
	det := detector.New(detector.DefaultConfig(cfg.ReserveSOC), snapshot)
	// Require two matching relay readings to prevent a transient or unknown
	// value from flashing an outage notification in the tray.
	outageDet := outage.New(2)
	slog.Info("detector initialized", "state", det.Snapshot().State, "reserve_soc", cfg.ReserveSOC, "poll_interval", cfg.PollInterval)

	initializePollInterval(cfg.PollInterval)
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	if err := pollOnce(ctx, client, det, outageDet, emailer, cfg.StatePath, cfg.Debug); err != nil {
		publishPollError(err)
		slog.Error("poll failed", "error", err)
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case interval := <-pollIntervalChanges:
				ticker.Reset(interval)
				slog.Info("poll interval changed", "poll_interval", interval)
			case <-ticker.C:
				if err := pollOnce(ctx, client, det, outageDet, emailer, cfg.StatePath, cfg.Debug); err != nil {
					publishPollError(err)
					slog.Error("poll failed", "error", err)
				}
			}
		}
	}()

	liveWindow, err := startLiveStatusWindow(cfg.ReserveSOC)
	if err != nil {
		slog.Error("start live status window", "error", err)
	}
	systray.Run(
		func() {
			onReady(stop, emailer, liveWindow)
		}, onExit)
	defer systray.Quit()
}

func initializePollInterval(interval time.Duration) {
	pollMu.Lock()
	runtimePollInterval = interval
	pollMu.Unlock()
}

func currentPollInterval() time.Duration {
	pollMu.RLock()
	defer pollMu.RUnlock()
	return runtimePollInterval
}

// setPollInterval applies a runtime-only override. It does not rewrite .env or
// trigger an immediate poll; the polling goroutine resets its ticker and waits
// for one complete interval.
func setPollInterval(interval time.Duration) {
	if interval <= 0 {
		return
	}
	pollMu.Lock()
	if runtimePollInterval == interval {
		pollMu.Unlock()
		return
	}
	runtimePollInterval = interval
	pollMu.Unlock()

	select {
	case pollIntervalChanges <- interval:
	default:
		select {
		case <-pollIntervalChanges:
		default:
		}
		pollIntervalChanges <- interval
	}
}

func pollOnce(ctx context.Context, client *gateway.Client, det *detector.Detector, outageDet *outage.Detector, emailer emailSender, statePath string, debug bool) error {
	// Debug mode captures the gateway's auxiliary endpoints for later analysis.
	// These calls are diagnostic only: a failure must not prevent the primary
	// live-data sample from driving detection and notifications.
	if debug {
		_, err := client.MeterDetails(ctx, debug)
		if err != nil {
			slog.Error("fetch meter details", "error", err)
		}

		_, err = client.MeterReadings(ctx, debug)
		if err != nil {
			slog.Error("fetch meter readings", "error", err)
		}

		_, err = client.ProductionMeterData(ctx, debug)
		if err != nil {
			slog.Error("fetch production meter data", "error", err)
		}

		_, err = client.EnergyData(ctx, debug)
		if err != nil {
			slog.Error("fetch energy data", "error", err)
		}

		_, err = client.InverterProductionData(ctx, debug)
		if err != nil {
			slog.Error("fetch inverter production data", "error", err)
		}

		_, err = client.PowerConsumptionData(ctx, debug)
		if err != nil {
			slog.Error("fetch power consumption data", "error", err)
		}

		_, err = client.GridReadings(ctx, debug)
		if err != nil {
			slog.Error("fetch inverter grid readings", "error", err)
		}
	}

	raw, err := client.LiveData(ctx, debug)
	if err != nil {
		return err
	}
	sample, err := gateway.Normalize(raw, time.Now())
	if err != nil {
		return err
	}

	result := det.Observe(sample)
	gridState := outageDet.Observe(sample.MainRelayState)
	gridOutage := gridState == outage.StateGridDown

	slog.Debug("sample",
		"state", result.State,
		"grid_outage", gridOutage,
		"grid_state", gridState,
		"main_relay", sample.MainRelayState,
		"soc", sample.SOC,
		"battery_w", sample.BatteryPowerW,
		"grid_w", sample.GridPowerW,
		"pv_w", sample.PVPowerW,
		"load_w", sample.LoadPowerW,
		"task_id", sample.Tasks.TaskID,
		"task_timestamp", sample.Tasks.Timestamp,
		"reason", result.Reason,
	)

	// Persist before notifying. If the process exits during notification, the
	// next run resumes the detector state instead of announcing a second start.
	if err := state.Save(statePath, det.Snapshot()); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	// Publish one coherent snapshot for the independently running tray updater
	// and live-status renderer.
	statusMu.Lock()
	previousGridState := Status.GridState
	Status.SOC = sample.SOC
	Status.State = result.State
	Status.BatteryPowerW = sample.BatteryPowerW
	Status.GridPowerW = sample.GridPowerW
	Status.LoadPowerW = sample.LoadPowerW
	Status.PVPowerW = sample.PVPowerW
	Status.MainRelayState = sample.MainRelayState
	Status.GridState = gridState
	Status.GridOutage = gridOutage
	Status.UpdatedAt = sample.At
	Status.HasSample = true
	Status.LastError = ""
	statusMu.Unlock()
	if gridState != previousGridState {
		switch gridState {
		case outage.StateGridDown:
			slog.Warn("grid outage detected", "main_relay_state", sample.MainRelayState)
		case outage.StateManualDisconnected:
			slog.Info("grid manually disconnected", "main_relay_state", sample.MainRelayState)
		case outage.StateConnected:
			slog.Info("grid connection restored", "main_relay_state", sample.MainRelayState)
		}
	}

	if err := notifyTransition(det, emailer, statePath, result); err != nil {
		return err
	}

	return nil
}

func publishPollError(err error) {
	statusMu.Lock()
	Status.LastError = err.Error()
	statusMu.Unlock()
}

func notifyTransition(det *detector.Detector, emailer emailSender, statePath string, result detector.Result) error {
	switch result.Transition {
	case detector.NoTransition:
		return nil
	case detector.Started, detector.Ended:
		// Handled below.
	default:
		return fmt.Errorf("unknown detector transition %q", result.Transition)
	}

	if emailer == nil {
		det.AcknowledgeTransition(result.Transition)
		if err := state.Save(statePath, det.Snapshot()); err != nil {
			return fmt.Errorf("save %s transition with notifications disabled: %w", result.Transition, err)
		}
		slog.Info("DR event transition detected", "transition", result.Transition, "notification", "disabled", "reason", result.Reason)
		return nil
	}

	var subject, body string
	if result.Transition == detector.Started {
		subject = "DR Event Started"
		body = startedBody(result)
	} else {
		subject = "DR Event Ended"
		body = endedBody(result)
	}

	if err := emailer.Send(subject, body); err != nil {
		return fmt.Errorf("send %s notification: %w", result.Transition, err)
	}
	det.AcknowledgeTransition(result.Transition)
	if err := state.Save(statePath, det.Snapshot()); err != nil {
		return fmt.Errorf("save acknowledged %s notification: %w", result.Transition, err)
	}
	slog.Info("DR event notification sent", "transition", result.Transition, "reason", result.Reason)
	return nil
}

func startedBody(result detector.Result) string {
	return fmt.Sprintf(`DR Event Started
Time: %s
SOC: %.1f%%
Battery Power: %.0f W
Grid Power: %.0f W
PV Power: %.0f W
Reason: %s
`, localTime(result.EventStart), result.StartSOC, result.Sample.BatteryPowerW, result.Sample.GridPowerW, result.Sample.PVPowerW, result.Reason)
}

func endedBody(result detector.Result) string {
	duration := result.EventEnd.Sub(result.EventStart).Round(time.Minute)
	return fmt.Sprintf(`DR Event Ended
Time: %s
Duration: %s
Start SOC: %.1f%%
End SOC: %.1f%%
Estimated Discharged: %.2f kWh
Reason: %s
`, localTime(result.EventEnd), duration, result.StartSOC, result.EndSOC, result.EstimatedDischargeWh/1000, result.Reason)
}

func localTime(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.Local().Format("2006-01-02 15:04:05")
}
