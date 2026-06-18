package main

import (
	"context"
	"fmt"
	"io"
	"log"
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
	"drlistener/internal/state"

	"github.com/getlantern/systray"
	"github.com/joho/godotenv"
)

const configFile = ".env"

var (
	Status struct {
		SOC           float64
		State         detector.State
		BatteryPowerW float64
		GridPowerW    float64
		PVPowerW      float64
		LoadPowerW    float64
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

	mw := io.MultiWriter(os.Stdout, logfile)

	log.SetOutput(mw)
	log.SetFlags(
		log.LstdFlags |
			log.Lmicroseconds |
			log.Lshortfile,
	)

	return nil
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

		log.Printf("received signal: %v", sig)

		systray.Quit()
	}()

	// Read .env file if it exists
	err := godotenv.Load(configFile)
	if err != nil {
		log.Fatalf("error loading env file: %v", err)
	}

	// Load the configuration
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	// Initialize logging to stdout and a file
	err = initLogging(cfg.Logfile)
	if err != nil {
		log.Fatal(err)
	}

	// Initialize the emailer
	emailer := notify.NewEmailer(cfg.SMTP)
	if cfg.TestEmailAndExit {
		if err := emailer.Send("DR Listener Test", "DR Listener SMTP configuration is working."); err != nil {
			log.Fatalf("send test email: %v", err)
		}
		log.Printf("test email sent to %s", cfg.SMTP.To)
		return
	}

	// Initialize the envoy client
	client, err := gateway.NewClient(cfg.GatewayURL, cfg.GatewayToken, cfg.AllowInsecureTLS)
	if err != nil {
		log.Fatalf("gateway client: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Grab diagnostic info for reference only
	info, err := client.Info(ctx)
	if err != nil {
		log.Printf("gateway /info failed: %v", err)
	} else {
		log.Printf("gateway info: %+v", info)
	}

	snapshot, err := state.Load(cfg.StatePath)
	if err != nil {
		log.Fatalf("load state: %v", err)
	}
	det := detector.New(detector.DefaultConfig(cfg.ReserveSOC), snapshot)
	log.Printf("detector initialized: state=%s reserve_soc=%d poll_interval=%s", det.Snapshot().State, cfg.ReserveSOC, cfg.PollInterval)

	if slices.Contains(os.Args, "test_smtp") {
		log.Print("Sending a test email")
		err = testSMTP(ctx, client, det, emailer)
		if err != nil {
			log.Fatal(err)
		}
		return
	}

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	if err := pollOnce(ctx, client, det, emailer, cfg.StatePath); err != nil {
		log.Printf("poll failed: %v", err)
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := pollOnce(ctx, client, det, emailer, cfg.StatePath); err != nil {
					log.Printf("poll failed: %v", err)
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

func testSMTP(ctx context.Context, client *gateway.Client, det *detector.Detector, emailer notify.Emailer) error {
	raw, err := client.LiveData(ctx)
	if err != nil {
		return err
	}
	sample, err := gateway.Normalize(raw, time.Now())
	if err != nil {
		return err
	}

	result := det.Observe(sample)
	log.Printf("sample state=%s soc=%.1f battery_w=%.0f grid_w=%.0f pv_w=%.0f load_w=%.0f reason=%q",
		result.State,
		sample.SOC,
		sample.BatteryPowerW,
		sample.GridPowerW,
		sample.PVPowerW,
		sample.LoadPowerW,
		result.Reason,
	)

	if err := emailer.Send("DR Event Started", testBody(result)); err != nil {
		return fmt.Errorf("send start notification: %w", err)
	}
	return nil
}

func pollOnce(ctx context.Context, client *gateway.Client, det *detector.Detector, emailer notify.Emailer, statePath string) error {
	raw, err := client.LiveData(ctx)
	if err != nil {
		return err
	}
	sample, err := gateway.Normalize(raw, time.Now())
	if err != nil {
		return err
	}

	result := det.Observe(sample)
	log.Printf("sample state=%s soc=%.1f battery_w=%.0f grid_w=%.0f pv_w=%.0f load_w=%.0f reason=%q",
		result.State,
		sample.SOC,
		sample.BatteryPowerW,
		sample.GridPowerW,
		sample.PVPowerW,
		sample.LoadPowerW,
		result.Reason,
	)

	if err := state.Save(statePath, det.Snapshot()); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	// Update the shared status so the systray icon knows
	statusMu.Lock()
	Status.SOC = sample.SOC
	Status.State = result.State
	Status.BatteryPowerW = result.Sample.BatteryPowerW
	Status.GridPowerW = result.Sample.GridPowerW
	Status.LoadPowerW = result.Sample.LoadPowerW
	Status.PVPowerW = result.Sample.PVPowerW
	statusMu.Unlock()

	switch result.Transition {
	case detector.Started:
		if err := emailer.Send("DR Event Started", startedBody(result)); err != nil {
			return fmt.Errorf("send start notification: %w", err)
		}
		log.Printf("DR event started: %s", result.Reason)
	case detector.Ended:
		if err := emailer.Send("DR Event Ended", endedBody(result)); err != nil {
			return fmt.Errorf("send end notification: %w", err)
		}
		log.Printf("DR event ended: %s", result.Reason)
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
