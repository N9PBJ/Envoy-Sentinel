package main

import (
	"context"
	"drlistener/internal/detector"
	"drlistener/internal/outage"
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
	switch {
	case soc < 16.5:
		return 0
	case soc < 49.5:
		return 1
	case soc < 83.5:
		return 2
	default:
		return 3
	}
}

func batteryStatus(w float64) string {
	switch {
	case w > 100:
		return fmt.Sprintf("Battery: %.1f kW Discharging", w/1000)
	case w < -100:
		return fmt.Sprintf("Battery: %.1f kW Charging", -w/1000)
	default:
		return "Battery: Idle"
	}
}

func gridStatus(gridState outage.State, mainRelayState int, powerW float64) string {
	switch gridState {
	case outage.StateGridDown:
		return "Grid: OUTAGE - Islanded"
	case outage.StateManualDisconnected:
		return "Grid: Manually Disconnected"
	}
	if mainRelayState == outage.RelayTransition {
		return "Grid: Reconnecting"
	}
	if powerW < -100 {
		return fmt.Sprintf("Grid: %.1f kW Exporting", -powerW/1000)
	}
	if powerW > 100 {
		return fmt.Sprintf("Grid: %.1f kW Importing", powerW/1000)
	}
	return "Grid: Idle"
}

func gridTooltip(gridState outage.State, powerW float64) string {
	if gridState == outage.StateGridDown {
		return "OUTAGE"
	}
	if gridState == outage.StateManualDisconnected {
		return "MANUAL DISCONNECT"
	}
	return fmt.Sprintf("%.1fkW", powerW/1000)
}

func statusSnapshot() liveStatusSnapshot {
	statusMu.RLock()
	defer statusMu.RUnlock()
	return Status
}

// trayUpdater refreshes the lightweight menu and icon once per second from the
// same published snapshot used by the independently animated live window.
func trayUpdater() {
	icons := [][]byte{battery0, battery33, battery66, battery100}
	var flash bool
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s := statusSnapshot()
		bucket := batteryBucket(s.SOC)
		if s.GridOutage {
			flash = !flash
			if flash {
				systray.SetIcon(outageICO)
			} else {
				systray.SetIcon(icons[bucket])
			}
		} else if s.State == detector.Active {
			flash = !flash
			if flash {
				systray.SetIcon(alertICO)
			} else {
				systray.SetIcon(icons[bucket])
			}
		} else {
			systray.SetIcon(icons[bucket])
		}

		mDREvent.SetTitle(fmt.Sprintf("DR Event: %s", s.State))
		mSOC.SetTitle(fmt.Sprintf("SOC: %.0f%%", s.SOC))
		mBattery.SetTitle(batteryStatus(s.BatteryPowerW))
		mGrid.SetTitle(gridStatus(s.GridState, s.MainRelayState, s.GridPowerW))
		mSolar.SetTitle(fmt.Sprintf("Solar: %.1f kW", s.PVPowerW/1000))
		mHouse.SetTitle(fmt.Sprintf("House: %.1f kW", s.LoadPowerW/1000))
		systray.SetTooltip(fmt.Sprintf("SOC %.0f%% | Grid %s | DR Event %s", s.SOC, gridTooltip(s.GridState, s.GridPowerW), s.State))
	}
}

// onReady builds the tray menu. Menu actions remain responsive because window
// work, test email delivery, and telemetry polling run outside systray's loop.
func onReady(cancel context.CancelFunc, emailer emailSender, liveWindow *liveStatusWindow) {
	systray.SetIcon(battery100)
	systray.SetTitle("Envoy Sentinel")
	systray.SetTooltip("Enphase DR Event Watcher")

	mLiveStatus := systray.AddMenuItem("Open Live Status", "Show live power flow")
	systray.AddSeparator()
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
			case <-mLiveStatus.ClickedCh:
				if liveWindow != nil {
					liveWindow.Show()
				}
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
					testEmailResult <- emailer.Send("Envoy Sentinel Test", fmt.Sprintf("Envoy Sentinel SMTP configuration is working.\n\nSent: %s\n", time.Now().Format(time.RFC1123)))
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
				if err := exec.Command("notepad.exe", configFile).Start(); err != nil {
					slog.Error("open config", "error", err)
				}
			}
		}
	}()

	go trayUpdater()
}

func onExit() {}
