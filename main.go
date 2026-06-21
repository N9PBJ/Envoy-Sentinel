package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"slices"
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
	// Status is the most recent normalized gateway reading. pollOnce writes it
	// under statusMu; the tray updater takes a snapshot under the same lock so
	// network polling never blocks on Windows UI work.
	Status struct {
		SOC            float64
		State          detector.State
		BatteryPowerW  float64
		GridPowerW     float64
		PVPowerW       float64
		LoadPowerW     float64
		MainRelayState int
		GridOutage     bool
	}
	statusMu sync.RWMutex
)

func initLogging(logFilename string) error {
	logfile, err := os.OpenFile(
		logFilename,
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0644,
	)
	if err != nil {
		return fmt.Errorf("error opening log file: %v", err)
	}

	// Keep console output for interactive runs while retaining a persistent log
	// for the tray application, which normally has no visible console.
	mw := io.MultiWriter(os.Stdout, logfile)
	handler := slog.NewTextHandler(mw, &slog.HandlerOptions{AddSource: true})
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

	sigs := make(chan os.Signal, 1)

	signal.Notify(
		sigs,
		os.Interrupt,
		syscall.SIGTERM,
	)

	go func() {
		sig := <-sigs

		slog.Info("received signal", "signal", sig)

		systray.Quit()
	}()

	// Read .env file if it exists
	err := godotenv.Load(configFile)
	if err != nil {
		fatal("error loading env file", "error", err)
	}

	// Load the configuration
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		fatal("config error", "error", err)
	}

	// Initialize logging to stdout and a file
	err = initLogging(cfg.Logfile)
	if err != nil {
		fatal("initialize logging", "error", err)
	}

	// Initialize the emailer
	emailer := notify.NewEmailer(cfg.SMTP)
	if cfg.TestEmailAndExit {
		if err := emailer.Send("DR Listener Test", "DR Listener SMTP configuration is working."); err != nil {
			fatal("send test email", "error", err)
		}
		slog.Info("test email sent", "recipient", cfg.SMTP.To)
		return
	}

	// Initialize the envoy client
	client, err := gateway.NewClient(cfg.GatewayURL, "", cfg.AllowInsecureTLS)
	if err != nil {
		fatal("gateway client", "error", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	if slices.Contains(os.Args, "test_smtp") {
		slog.Info("sending a test email")
		err = testSMTP(ctx, client, det, emailer, cfg.Debug)
		if err != nil {
			fatal("test SMTP", "error", err)
		}
		return
	}

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	if err := pollOnce(ctx, client, det, outageDet, emailer, cfg.StatePath, cfg.Debug); err != nil {
		slog.Error("poll failed", "error", err)
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := pollOnce(ctx, client, det, outageDet, emailer, cfg.StatePath, cfg.Debug); err != nil {
					slog.Error("poll failed", "error", err)
				}
			}
		}
	}()

	systray.Run(
		func() {
			onReady(stop)
		}, onExit)
	defer systray.Quit()

}

func testSMTP(ctx context.Context, client *gateway.Client, det *detector.Detector, emailer notify.Emailer, debug bool) error {
	raw, err := client.LiveData(ctx, debug)
	if err != nil {
		return err
	}
	sample, err := gateway.Normalize(raw, time.Now())
	if err != nil {
		return err
	}

	result := det.Observe(sample)
	slog.Info("sample",
		"state", result.State,
		"soc", sample.SOC,
		"battery_w", sample.BatteryPowerW,
		"grid_w", sample.GridPowerW,
		"pv_w", sample.PVPowerW,
		"load_w", sample.LoadPowerW,
		"reason", result.Reason,
	)

	if err := emailer.Send("DR Event Started", testBody(result)); err != nil {
		return fmt.Errorf("send start notification: %w", err)
	}
	return nil
}

func pollOnce(ctx context.Context, client *gateway.Client, det *detector.Detector, outageDet *outage.Detector, emailer notify.Emailer, statePath string, debug bool) error {
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
	gridOutage := outageDet.Observe(sample.MainRelayState)

	slog.Info("sample",
		"state", result.State,
		"grid_outage", gridOutage,
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

	// Publish one coherent snapshot for the independently running tray updater.
	statusMu.Lock()
	previousGridOutage := Status.GridOutage
	Status.SOC = sample.SOC
	Status.State = result.State
	Status.BatteryPowerW = result.Sample.BatteryPowerW
	Status.GridPowerW = result.Sample.GridPowerW
	Status.LoadPowerW = result.Sample.LoadPowerW
	Status.PVPowerW = result.Sample.PVPowerW
	Status.MainRelayState = sample.MainRelayState
	Status.GridOutage = gridOutage
	statusMu.Unlock()
	if gridOutage != previousGridOutage {
		if gridOutage {
			slog.Warn("grid outage detected", "main_relay_state", sample.MainRelayState)
		} else {
			slog.Info("grid connection restored", "main_relay_state", sample.MainRelayState)
		}
	}

	switch result.Transition {
	case detector.Started:
		if err := emailer.Send("DR Event Started", startedBody(result)); err != nil {
			return fmt.Errorf("send start notification: %w", err)
		}
		slog.Info("DR event started", "reason", result.Reason)
	case detector.Ended:
		if err := emailer.Send("DR Event Ended", endedBody(result)); err != nil {
			return fmt.Errorf("send end notification: %w", err)
		}
		slog.Info("DR event ended", "reason", result.Reason)
	}

	return nil
}

func testBody(result detector.Result) string {
	return fmt.Sprintf(`
Current State: %s
SOC: %.1f%%
Battery Power: %.0f W
Grid Power: %.0f W
PV Power: %.0f W
`,
		result.State,
		result.Sample.SOC,
		result.Sample.BatteryPowerW,
		result.Sample.GridPowerW,
		result.Sample.PVPowerW,
	)
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
