package main

import (
	"context"
	"drlistener/internal/detector"
	_ "embed"
	"fmt"
	"log"
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
	switch {
	case w > 100:
		return fmt.Sprintf("Battery: %.1f kW Discharging", w/1000)
	case w < -100:
		return fmt.Sprintf("Battery: %.1f kW Charging", -w/1000)
	default:
		return "Battery: Idle"
	}
}

func trayUpdater() {
	var icons = [][]byte{
		battery0,
		battery33,
		battery66,
		battery100,
	}

	var flash bool

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		statusMu.RLock()
		s := Status
		statusMu.RUnlock()

		socBucket := batteryBucket(s.SOC)

		if s.State == detector.Active {
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

		mGrid.SetTitle(
			fmt.Sprintf("Grid: %.1f kW", s.GridPowerW/1000),
		)

		mSolar.SetTitle(
			fmt.Sprintf("Solar: %.1f kW", s.PVPowerW/1000),
		)

		mHouse.SetTitle(
			fmt.Sprintf("House: %.1f kW", s.LoadPowerW/1000),
		)

		systray.SetTooltip(
			fmt.Sprintf(
				"SOC %.0f%% | Grid %.1fkW | DR Event %s",
				s.SOC,
				s.GridPowerW/1000,
				s.State,
			),
		)
	}
}

// func trayUpdater() {
// 	var (
// 		icons = [][]byte{
// 			battery0,
// 			battery33,
// 			battery66,
// 			battery100,
// 			alertICO,
// 		}

// 		lastSOC   = -1
// 		lastState detector.State
// 		flash     bool
// 	)

// 	ticker := time.NewTicker(5 * time.Second)

// 	for range ticker.C {
// 		statusMu.RLock()
// 		s := Status
// 		statusMu.RUnlock()

// 		socBucket := batteryBucket(s.SOC)

// 		if socBucket != lastSOC || s.State != lastState {
// 			log.Printf("updating tray icon to SOC %d", socBucket)

// 			systray.SetIcon(icons[batteryBucket(Status.SOC)])

// 			lastSOC = socBucket
// 			lastState = s.State
// 		}

// 		mDREvent.SetTitle(
// 			fmt.Sprintf("DR Event: %s", s.State),
// 		)

// 		mSOC.SetTitle(
// 			fmt.Sprintf("SOC: %.0f%%", s.SOC),
// 		)

// 		mBattery.SetTitle(batteryStatus(s.BatteryPowerW))

// 		mGrid.SetTitle(
// 			fmt.Sprintf("Grid: %.1f kW", s.GridPowerW/1000),
// 		)

// 		mSolar.SetTitle(
// 			fmt.Sprintf("Solar: %.1f kW", s.PVPowerW/1000),
// 		)

// 		mHouse.SetTitle(
// 			fmt.Sprintf("House: %.1f kW", s.LoadPowerW/1000),
// 		)

// 		systray.SetTooltip(
// 			fmt.Sprintf(
// 				"SOC %.0f%% | Grid %.1fkW | DR Event %s",
// 				s.SOC,
// 				s.GridPowerW/1000,
// 				s.State,
// 			),
// 		)
// 	}
// }

func onReady(cancel context.CancelFunc) {
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

	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "")

	go func() {
		for {
			select {
			case <-mQuit.ClickedCh:
				cancel()
				systray.Quit()
				return
			case <-mConfig.ClickedCh:
				err := exec.Command(
					"notepad.exe",
					configFile,
				).Start()

				if err != nil {
					log.Printf("open config: %v", err)
				}
			}
		}
	}()

	go trayUpdater()
}

func onExit() {
}
