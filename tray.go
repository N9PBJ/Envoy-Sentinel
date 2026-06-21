package main

import (
	"context"
	"drlistener/internal/detector"
	_ "embed"
	"fmt"
	"log/slog"
	"os/exec"
	"time"

	"github.com/getlantern/systray"
)

//go:embed battery-0.ico
var battery0 []byte

//go:embed battery-33.ico
var battery33 []byte

//go:embed battery-66.ico
var battery66 []byte

//go:embed battery-100.ico
var battery100 []byte

//go:embed alert.ico
var alertICO []byte

//go:embed outage.ico
var outageICO []byte

var (
	mSOC     *systray.MenuItem
	mBattery *systray.MenuItem
	mGrid    *systray.MenuItem
	mSolar   *systray.MenuItem
	mHouse   *systray.MenuItem
	mDREvent *systray.MenuItem
)

func batteryBucket(soc float64) int {
	// Icons are available at 0, 33, 66, and 100 percent. Midpoints make the
	// selected icon the closest visual representation of the actual SOC.
	switch {
	case soc < 16.5:
		return 0 // 0%

	case soc < 49.5:
		return 1 // 33%

	case soc < 83.5:
		return 2 // 66%

	default:
		return 3 // 100%
	}
}

func batteryStatus(w float64) string {
	// Gateway normalization uses positive battery power for discharge and
	// negative power for charge. Ignore +/-100 W as measurement noise/idle.
	switch {
	case w > 100:
		return fmt.Sprintf("Battery: %.1f kW Discharging", w/1000)
	case w < -100:
		return fmt.Sprintf("Battery: %.1f kW Charging", -w/1000)
	default:
		return "Battery: Idle"
	}
}

func gridStatus(outage bool, mainRelayState int, powerW float64) string {
	if outage {
		return "Grid: OUTAGE — Islanded"
	}
	if mainRelayState == 3 {
		return "Grid: Reconnecting"
	}
	return fmt.Sprintf("Grid: %.1f kW", powerW/1000)
}

func gridTooltip(outage bool, powerW float64) string {
	if outage {
		return "OUTAGE"
	}
	return fmt.Sprintf("%.1fkW", powerW/1000)
}

func trayUpdater() {
	var icons = [][]byte{
		battery0,
		battery33,
		battery66,
		battery100,
	}

	var flash bool

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		statusMu.RLock()
		s := Status
		statusMu.RUnlock()

		socBucket := batteryBucket(s.SOC)

		// An outage takes visual priority over a DR event. Flashing against the
		// battery icon preserves SOC visibility while still attracting attention.
		if s.GridOutage {
			flash = !flash

			if flash {
				systray.SetIcon(outageICO)
			} else {
				systray.SetIcon(icons[socBucket])
			}
		} else if s.State == detector.Active {
			flash = !flash

			if flash {
				systray.SetIcon(alertICO)
			} else {
				systray.SetIcon(icons[socBucket])
			}
		} else {
			systray.SetIcon(icons[socBucket])
		}

		mDREvent.SetTitle(
			fmt.Sprintf("DR Event: %s", s.State),
		)

		mSOC.SetTitle(
			fmt.Sprintf("SOC: %.0f%%", s.SOC),
		)

		mBattery.SetTitle(batteryStatus(s.BatteryPowerW))

		mGrid.SetTitle(gridStatus(s.GridOutage, s.MainRelayState, s.GridPowerW))

		mSolar.SetTitle(
			fmt.Sprintf("Solar: %.1f kW", s.PVPowerW/1000),
		)

		mHouse.SetTitle(
			fmt.Sprintf("House: %.1f kW", s.LoadPowerW/1000),
		)

		systray.SetTooltip(
			fmt.Sprintf(
				"SOC %.0f%% | Grid %s | DR Event %s",
				s.SOC,
				gridTooltip(s.GridOutage, s.GridPowerW),
				s.State,
			),
		)
	}
}

func onReady(cancel context.CancelFunc, emailer emailSender) {
	systray.SetIcon(battery100)
	systray.SetTitle("DR Event Notifier")
	systray.SetTooltip("Envoy / GVEC DR Event Watcher")

	mDREvent = systray.AddMenuItem("DR Event: --", "")
	mDREvent.Disable()

	mSOC = systray.AddMenuItem("SOC: --", "")
	mSOC.Disable()

	mBattery = systray.AddMenuItem("Battery: --", "")
	mBattery.Disable()

	mGrid = systray.AddMenuItem("Grid: --", "")
	mGrid.Disable()

	mSolar = systray.AddMenuItem("Solar: --", "")
	mSolar.Disable()

	mHouse = systray.AddMenuItem("House: --", "")
	mHouse.Disable()

	systray.AddSeparator()
	mConfig := systray.AddMenuItem("Open Config", "")
	mTestEmail := systray.AddMenuItem("Send Test Email...", "Click twice to confirm")
	if emailer == nil {
		mTestEmail.SetTitle("Email Notifications Disabled")
		mTestEmail.Disable()
	}

	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "")

	go func() {
		const confirmationWindow = 10 * time.Second
		var confirmationTimer *time.Timer
		var confirmationExpired <-chan time.Time
		testEmailResult := make(chan error, 1)

		for {
			select {
			case <-mQuit.ClickedCh:
				if confirmationTimer != nil {
					confirmationTimer.Stop()
				}
				cancel()
				systray.Quit()
				return
			case <-mTestEmail.ClickedCh:
				if emailer == nil {
					continue
				}
				if confirmationExpired == nil {
					mTestEmail.SetTitle("Confirm Send Test Email")
					confirmationTimer = time.NewTimer(confirmationWindow)
					confirmationExpired = confirmationTimer.C
					continue
				}

				confirmationTimer.Stop()
				confirmationExpired = nil
				mTestEmail.SetTitle("Sending Test Email...")
				mTestEmail.Disable()
				go func() {
					testEmailResult <- emailer.Send(
						"DR Listener Test",
						fmt.Sprintf("DR Listener SMTP configuration is working.\n\nSent: %s\n", time.Now().Format(time.RFC1123)),
					)
				}()
			case <-confirmationExpired:
				confirmationExpired = nil
				confirmationTimer = nil
				mTestEmail.SetTitle("Send Test Email...")
			case err := <-testEmailResult:
				mTestEmail.Enable()
				if err != nil {
					mTestEmail.SetTitle("Test Email Failed - Click to Retry")
					slog.Error("send test email", "error", err)
				} else {
					mTestEmail.SetTitle("Test Email Sent - Click to Send Again")
					slog.Info("test email sent")
				}
			case <-mConfig.ClickedCh:
				err := exec.Command(
					"notepad.exe",
					configFile,
				).Start()

				if err != nil {
					slog.Error("open config", "error", err)
				}
			}
		}
	}()

	go trayUpdater()
}

func onExit() {
}
