//go:build windows

package main

import (
	"drlistener/internal/detector"
	"drlistener/internal/outage"
	"fmt"
	"log/slog"
	"math"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/lxn/walk"
	"github.com/lxn/walk/declarative"
)

// liveStatusSessionDuration mirrors the official live view's bounded viewing
// session. Expiry hides only this window; monitoring continues in main.
const liveStatusSessionDuration = 15 * time.Minute

var (
	liveWindowMu sync.RWMutex
	liveWindow   *liveStatusWindow
)

// liveStatusWindow owns the Walk controls and viewing-session state. Walk UI
// operations run on its locked OS thread; mu protects fields read by the paint
// callback and the refresh goroutine.
type liveStatusWindow struct {
	mw     *walk.MainWindow
	canvas *walk.CustomWidget
	poll   *walk.ComboBox

	mu      sync.Mutex
	expires time.Time
	visible bool
	closing bool
	phase   float64
	done    chan struct{}

	font11      *walk.Font
	font12      *walk.Font
	font14      *walk.Font
	font15      *walk.Font
	font16      *walk.Font
	font20Bold  *walk.Font
	font20      *walk.Font
	reserveSOC  int
	pollOptions []time.Duration
}

type liveWindowResult struct {
	window *liveStatusWindow
	err    error
}

// startLiveStatusWindow creates the native window on a dedicated, locked OS
// thread and waits until construction succeeds or fails. The embedded Windows
// Common Controls v6 manifest is required by Walk during this initialization.
func startLiveStatusWindow(reserveSOC int) (*liveStatusWindow, error) {
	ready := make(chan liveWindowResult, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		w := &liveStatusWindow{
			expires:    time.Now().Add(liveStatusSessionDuration),
			visible:    true,
			done:       make(chan struct{}),
			reserveSOC: reserveSOC,
		}
		if err := w.create(); err != nil {
			ready <- liveWindowResult{err: err}
			return
		}
		liveWindowMu.Lock()
		liveWindow = w
		liveWindowMu.Unlock()
		ready <- liveWindowResult{window: w}

		go w.refreshLoop()
		w.mw.Show()
		w.mw.Run()
		close(w.done)
		liveWindowMu.Lock()
		if liveWindow == w {
			liveWindow = nil
		}
		liveWindowMu.Unlock()
	}()

	result := <-ready
	return result.window, result.err
}

func (w *liveStatusWindow) create() error {
	var err error
	fonts := []struct {
		target **walk.Font
		size   int
		style  walk.FontStyle
	}{
		{&w.font11, 8, 0}, {&w.font12, 9, walk.FontBold},
		{&w.font14, 10, 0}, {&w.font15, 11, 0},
		{&w.font16, 12, 0}, {&w.font20Bold, 15, walk.FontBold},
		{&w.font20, 15, 0},
	}
	for _, item := range fonts {
		*item.target, err = walk.NewFont("Segoe UI", item.size, item.style)
		if err != nil {
			return err
		}
	}

	w.pollOptions = pollIntervalOptions(currentPollInterval())
	pollLabels := make([]string, len(w.pollOptions))
	currentPollIndex := 0
	for i, interval := range w.pollOptions {
		pollLabels[i] = formatPollInterval(interval)
		if interval == currentPollInterval() {
			currentPollIndex = i
		}
	}

	err = (declarative.MainWindow{
		AssignTo: &w.mw,
		Title:    "Envoy Sentinel — Live Status",
		Layout:   declarative.VBox{MarginsZero: true, SpacingZero: true},
		Children: []declarative.Widget{
			declarative.CustomWidget{
				AssignTo:            &w.canvas,
				InvalidatesOnResize: true,
				PaintMode:           declarative.PaintBuffered,
				Paint:               w.paint,
				StretchFactor:       1,
			},
			declarative.Composite{
				MinSize: declarative.Size{Height: 46},
				MaxSize: declarative.Size{Height: 46},
				Layout: declarative.HBox{
					Margins: declarative.Margins{Left: 12, Top: 7, Right: 12, Bottom: 7},
					Spacing: 8,
				},
				Children: []declarative.Widget{
					declarative.Label{
						Text:      "Controller polling:",
						Alignment: declarative.AlignHNearVCenter,
						MinSize:   declarative.Size{Width: 115},
					},
					declarative.ComboBox{
						AssignTo:     &w.poll,
						Model:        pollLabels,
						CurrentIndex: currentPollIndex,
						MinSize:      declarative.Size{Width: 125},
						OnCurrentIndexChanged: func() {
							if w.poll == nil {
								return
							}
							index := w.poll.CurrentIndex()
							if index >= 0 && index < len(w.pollOptions) {
								setPollInterval(w.pollOptions[index])
							}
						},
					},
					declarative.HSpacer{},
					declarative.Label{
						Text:          "Runtime only · startup default remains in .env",
						TextColor:     walk.RGB(115, 125, 140),
						TextAlignment: declarative.AlignFar,
						Alignment:     declarative.AlignHFarVCenter,
					},
				},
			},
		},
	}).Create()
	if err != nil {
		return err
	}
	if err := w.mw.SetClientSize(walk.Size{Width: 560, Height: 736}); err != nil {
		return err
	}
	w.mw.Closing().Attach(func(canceled *bool, _ walk.CloseReason) {
		w.mu.Lock()
		closing := w.closing
		if !closing {
			w.visible = false
		}
		w.mu.Unlock()
		if !closing {
			*canceled = true
			w.mw.Hide()
		}
	})
	return nil
}

func (w *liveStatusWindow) Show() {
	w.mw.Synchronize(func() {
		w.mu.Lock()
		w.expires = time.Now().Add(liveStatusSessionDuration)
		w.visible = true
		w.mu.Unlock()
		w.mw.Show()
		_ = w.mw.SetFocus()
		_ = w.canvas.Invalidate()
	})
}

// Close performs the real shutdown close. The title-bar close handler normally
// cancels closure and hides the window so tray monitoring remains available.
func (w *liveStatusWindow) Close() {
	w.mw.Synchronize(func() {
		w.mu.Lock()
		w.closing = true
		w.mu.Unlock()
		err := w.mw.Close()
		if err != nil {
			slog.Error("close live status window", "error", err)
		}
	})
}

func quitLiveStatusWindow() {
	liveWindowMu.RLock()
	w := liveWindow
	liveWindowMu.RUnlock()
	if w != nil {
		w.Close()
	}
}

func (w *liveStatusWindow) refreshLoop() {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-w.done:
			return
		case now := <-ticker.C:
			w.mu.Lock()
			w.phase = math.Mod(w.phase+0.125, 1)
			expired := w.visible && !now.Before(w.expires)
			if expired {
				w.visible = false
			}
			w.mu.Unlock()
			w.mw.Synchronize(func() {
				if expired {
					w.mw.Hide()
				}
				_ = w.canvas.Invalidate()
			})
		}
	}
}

func (w *liveStatusWindow) paint(c *walk.Canvas, _ walk.Rectangle) error {
	r := &liveStatusRenderer{canvas: c, dpi: c.DPI()}
	bounds := w.canvas.ClientBounds()
	r.fillRectangle(walk.RGB(255, 255, 255), bounds)

	s := statusSnapshot()
	w.mu.Lock()
	remaining := time.Until(w.expires)
	visible := w.visible
	phase := w.phase
	w.mu.Unlock()

	ink := walk.RGB(60, 61, 65)
	muted := walk.RGB(139, 151, 171)
	border := walk.RGB(215, 220, 225)
	flow := walk.RGB(0, 177, 225)
	flowIdle := walk.RGB(207, 224, 231)
	green := walk.RGB(87, 202, 32)
	orange := walk.RGB(255, 103, 24)
	grid := walk.RGB(108, 116, 121)
	danger := walk.RGB(215, 53, 47)

	r.drawLine(border, 1, 0, 60, bounds.Width, 60)
	r.text("Live Status", w.font20Bold, ink, walk.Rectangle{X: 23, Y: 13, Width: 120, Height: 35}, false)
	countdown := "Live view paused"
	if visible {
		countdown = "Closing in " + formatCountdown(remaining)
	}
	r.text(countdown, w.font11, flow, walk.Rectangle{X: 137, Y: 17, Width: 210, Height: 26}, false)

	connection, connectionColor := connectionPresentation(s)
	r.fillCircle(connectionColor, walk.Rectangle{X: 17, Y: 94, Width: 9, Height: 9})
	r.text(connection, w.font14, ink, walk.Rectangle{X: 32, Y: 84, Width: 500, Height: 30}, false)

	r.fillRectangle(walk.RGB(252, 253, 254), walk.Rectangle{X: 8, Y: 132, Width: 544, Height: 80})
	r.drawRect(border, walk.Rectangle{X: 8, Y: 132, Width: 544, Height: 80})
	r.drawLine(border, 1, 278, 148, 278, 195)
	r.drawBatteryGlyph(green, 24, 157, 22, 31)
	r.text("CHARGE", w.font12, muted, walk.Rectangle{X: 54, Y: 151, Width: 180, Height: 22}, false)
	charge := "--%"
	if s.HasSample {
		charge = fmt.Sprintf("%.0f%%", s.SOC)
	}
	r.text(charge, w.font16, ink, walk.Rectangle{X: 54, Y: 172, Width: 180, Height: 26}, false)
	r.drawProfileGlyph(flow, 286, 160)
	r.text("PROFILE", w.font12, muted, walk.Rectangle{X: 319, Y: 151, Width: 210, Height: 22}, false)
	r.text(profileName(s.State), w.font15, flow, walk.Rectangle{X: 319, Y: 173, Width: 220, Height: 26}, false)

	r.drawSolar(flow, 280, 269)
	r.centerText(samplePower(s.HasSample, s.PVPowerW), w.font20Bold, flow, 190, 296, 180, 28)
	solarCaption := "Solar"
	if s.HasSample {
		solarCaption = powerState(s.PVPowerW, "Producing", "Idle")
	}
	r.centerText(solarCaption, w.font15, ink, 190, 320, 180, 24)

	r.drawFlow(flow, flowIdle, s.HasSample && s.PVPowerW > 100, 280, 344, 280, 442, phase)
	if s.GridPowerW < -100 {
		r.drawFlow(flow, flowIdle, s.HasSample, 280, 446, 133, 446, phase)
	} else {
		r.drawFlow(flow, flowIdle, s.HasSample && s.GridPowerW > 100, 133, 446, 280, 446, phase)
	}
	r.drawFlow(flow, flowIdle, s.HasSample && s.LoadPowerW > 100, 280, 446, 421, 446, phase)
	if s.BatteryPowerW > 100 {
		r.drawFlow(flow, flowIdle, s.HasSample, 280, 579, 280, 446, phase)
	} else {
		r.drawFlow(flow, flowIdle, s.HasSample && s.BatteryPowerW < -100, 280, 446, 280, 579, phase)
	}
	r.fillCircle(flow, walk.Rectangle{X: 276, Y: 442, Width: 8, Height: 8})

	r.drawGrid(grid, 111, 450)
	gridValue, gridState := gridPresentation(s.GridState, s.MainRelayState, s.GridPowerW)
	if !s.HasSample {
		gridValue, gridState = "-- kW", "Grid"
	}
	r.centerText(gridValue, w.font20Bold, grid, 35, 483, 160, 28)
	r.centerText(gridState, w.font15, ink, 35, 507, 160, 24)

	r.drawHouse(orange, 443, 450)
	r.centerText(samplePower(s.HasSample, s.LoadPowerW), w.font20Bold, orange, 366, 483, 155, 28)
	houseCaption := "House"
	if s.HasSample {
		houseCaption = powerState(s.LoadPowerW, "Consuming", "Idle")
	}
	r.centerText(houseCaption, w.font15, ink, 366, 507, 155, 24)

	r.drawStorage(green, 280, 603)
	batteryValue, batteryState := batteryPresentation(s.BatteryPowerW)
	if !s.HasSample {
		batteryValue, batteryState = "-- kW", "Battery"
	}
	r.centerText(batteryValue, w.font20Bold, green, 190, 631, 180, 28)
	r.centerText(batteryState, w.font15, ink, 190, 654, 180, 24)

	freshness := "Waiting for gateway data…"
	if s.HasSample {
		freshness = "Updated " + formatFreshness(s.UpdatedAt, time.Now())
	}
	if s.LastError != "" {
		freshness += " · latest poll failed"
	}
	r.text(freshness, w.font11, muted, walk.Rectangle{X: 12, Y: 668, Width: 400, Height: 18}, false)
	if s.GridOutage {
		r.text("OUTAGE", w.font12, danger, walk.Rectangle{X: 470, Y: 668, Width: 75, Height: 18}, true)
	}
	return r.err
}

// liveStatusRenderer draws from a 560-by-690 logical design grid. Its helpers
// convert those 96-DPI coordinates to native pixels and retain the first GDI
// error so the Walk paint callback can report it without panicking.
type liveStatusRenderer struct {
	canvas *walk.Canvas
	dpi    int
	err    error
}

func (r *liveStatusRenderer) record(err error) {
	if r.err == nil && err != nil {
		r.err = err
	}
}

func (r *liveStatusRenderer) rectangle(rect walk.Rectangle) walk.Rectangle {
	return walk.RectangleFrom96DPI(rect, r.dpi)
}

func (r *liveStatusRenderer) point(x, y int) walk.Point {
	return walk.PointFrom96DPI(walk.Point{X: x, Y: y}, r.dpi)
}

func (r *liveStatusRenderer) text(value string, font *walk.Font, clr walk.Color, rect walk.Rectangle, centered bool) {
	if r.err != nil {
		return
	}
	format := walk.TextSingleLine | walk.TextVCenter
	if centered {
		format |= walk.TextCenter
	} else {
		format |= walk.TextLeft
	}
	r.record(r.canvas.DrawTextPixels(value, font, clr, r.rectangle(rect), format))
}

func (r *liveStatusRenderer) centerText(value string, font *walk.Font, clr walk.Color, x, y, width, height int) {
	r.text(value, font, clr, walk.Rectangle{X: x, Y: y, Width: width, Height: height}, true)
}

func (r *liveStatusRenderer) fillRectangle(clr walk.Color, rect walk.Rectangle) {
	if r.err != nil {
		return
	}
	brush, err := walk.NewSolidColorBrush(clr)
	if err != nil {
		r.record(err)
		return
	}
	defer brush.Dispose()
	r.record(r.canvas.FillRectanglePixels(brush, r.rectangle(rect)))
}

func (r *liveStatusRenderer) drawLine(clr walk.Color, width, x1, y1, x2, y2 int) {
	if r.err != nil {
		return
	}
	brush, err := walk.NewSolidColorBrush(clr)
	if err != nil {
		r.record(err)
		return
	}
	defer brush.Dispose()
	pen, err := walk.NewGeometricPen(walk.PenSolid, width, brush)
	if err != nil {
		r.record(err)
		return
	}
	defer pen.Dispose()
	r.record(r.canvas.DrawLinePixels(pen, r.point(x1, y1), r.point(x2, y2)))
}

func (r *liveStatusRenderer) drawRect(clr walk.Color, rect walk.Rectangle) {
	if r.err != nil {
		return
	}
	pen, err := walk.NewCosmeticPen(walk.PenSolid, clr)
	if err != nil {
		r.record(err)
		return
	}
	defer pen.Dispose()
	r.record(r.canvas.DrawRectanglePixels(pen, r.rectangle(rect)))
}

func (r *liveStatusRenderer) fillCircle(clr walk.Color, rect walk.Rectangle) {
	if r.err != nil {
		return
	}
	brush, err := walk.NewSolidColorBrush(clr)
	if err != nil {
		r.record(err)
		return
	}
	defer brush.Dispose()
	r.record(r.canvas.FillEllipsePixels(brush, r.rectangle(rect)))
}

func (r *liveStatusRenderer) outlineCircle(clr walk.Color, rect walk.Rectangle) {
	if r.err != nil {
		return
	}
	pen, err := walk.NewCosmeticPen(walk.PenSolid, clr)
	if err != nil {
		r.record(err)
		return
	}
	defer pen.Dispose()
	r.record(r.canvas.DrawEllipsePixels(pen, r.rectangle(rect)))
}

func (r *liveStatusRenderer) drawFlow(active, idle walk.Color, enabled bool, x1, y1, x2, y2 int, phase float64) {
	clr := idle
	if enabled {
		clr = active
	}
	r.drawLine(clr, 2, x1, y1, x2, y2)
	if enabled {
		x := x1 + int(float64(x2-x1)*phase)
		y := y1 + int(float64(y2-y1)*phase)
		r.fillCircle(active, walk.Rectangle{X: x - 4, Y: y - 4, Width: 8, Height: 8})
	}
}

func (r *liveStatusRenderer) drawSolar(clr walk.Color, x, y int) {
	r.outlineCircle(clr, walk.Rectangle{X: x - 23, Y: y - 23, Width: 46, Height: 46})
	r.outlineCircle(clr, walk.Rectangle{X: x - 7, Y: y - 7, Width: 14, Height: 14})
	for _, line := range [][4]int{{0, -17, 0, -11}, {0, 11, 0, 17}, {-17, 0, -11, 0}, {11, 0, 17, 0}, {-12, -12, -8, -8}, {8, 8, 12, 12}, {12, -12, 8, -8}, {-8, 8, -12, 12}} {
		r.drawLine(clr, 1, x+line[0], y+line[1], x+line[2], y+line[3])
	}
}

func (r *liveStatusRenderer) drawGrid(clr walk.Color, x, y int) {
	r.outlineCircle(clr, walk.Rectangle{X: x - 25, Y: y - 25, Width: 50, Height: 50})
	r.drawLine(clr, 2, x, y-15, x-10, y+17)
	r.drawLine(clr, 2, x, y-15, x+10, y+17)
	r.drawLine(clr, 1, x-7, y-6, x+7, y-6)
	r.drawLine(clr, 1, x-10, y+4, x+10, y+4)
	r.drawLine(clr, 1, x-13, y+17, x, y+4)
	r.drawLine(clr, 1, x+13, y+17, x, y+4)
}

func (r *liveStatusRenderer) drawHouse(clr walk.Color, x, y int) {
	r.outlineCircle(clr, walk.Rectangle{X: x - 25, Y: y - 25, Width: 50, Height: 50})
	r.drawLine(clr, 2, x-12, y-1, x, y-12)
	r.drawLine(clr, 2, x, y-12, x+12, y-1)
	r.drawLine(clr, 2, x-9, y-4, x-9, y+13)
	r.drawLine(clr, 2, x+9, y-4, x+9, y+13)
	r.drawLine(clr, 2, x-9, y+13, x+9, y+13)
	r.drawRect(clr, walk.Rectangle{X: x - 2, Y: y + 4, Width: 5, Height: 9})
}

func (r *liveStatusRenderer) drawStorage(clr walk.Color, x, y int) {
	r.outlineCircle(clr, walk.Rectangle{X: x - 25, Y: y - 25, Width: 50, Height: 50})
	r.drawRect(clr, walk.Rectangle{X: x - 13, Y: y - 7, Width: 25, Height: 14})
	r.drawRect(clr, walk.Rectangle{X: x + 12, Y: y - 3, Width: 3, Height: 6})
}

func (r *liveStatusRenderer) drawBatteryGlyph(clr walk.Color, x, y, width, height int) {
	r.drawRect(clr, walk.Rectangle{X: x + 3, Y: y, Width: width - 6, Height: 3})
	r.drawRect(clr, walk.Rectangle{X: x, Y: y + 3, Width: width, Height: height - 3})
	for offset := 9; offset < height-3; offset += 6 {
		r.drawLine(clr, 1, x+4, y+offset, x+width-4, y+offset)
	}
}

func (r *liveStatusRenderer) drawProfileGlyph(clr walk.Color, x, y int) {
	r.fillCircle(clr, walk.Rectangle{X: x, Y: y, Width: 25, Height: 25})
	white := walk.RGB(255, 255, 255)
	r.fillCircle(white, walk.Rectangle{X: x + 8, Y: y + 8, Width: 9, Height: 9})
}

func formatCountdown(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int(math.Ceil(d.Seconds()))
	return fmt.Sprintf("%02d:%02d", seconds/60, seconds%60)
}

func samplePower(hasSample bool, watts float64) string {
	if !hasSample {
		return "-- kW"
	}
	return formatKW(watts)
}

func formatKW(w float64) string { return fmt.Sprintf("%.1f kW", math.Abs(w)/1000) }

func powerState(w float64, active, idle string) string {
	if math.Abs(w) <= 100 {
		return idle
	}
	return active
}

func profileName(state detector.State) string {
	if state == detector.Active || state == detector.SuspectActive {
		return "Demand Response"
	}
	return "Self-Consumption"
}

func gridPresentation(state outage.State, relay int, w float64) (string, string) {
	switch state {
	case outage.StateGridDown:
		return "OFF GRID", "Islanded"
	case outage.StateManualDisconnected:
		return "OFF GRID", "Disconnected"
	}
	if relay == outage.RelayTransition {
		return "-- kW", "Reconnecting"
	}
	if w < -100 {
		return formatKW(w), "Exporting"
	}
	if w > 100 {
		return formatKW(w), "Importing"
	}
	return "0.0 kW", "Idle"
}

func batteryPresentation(w float64) (string, string) {
	if w > 100 {
		return formatKW(w), "Discharging"
	}
	if w < -100 {
		return formatKW(w), "Charging"
	}
	return "0.0 kW", "Idle"
}

func connectionPresentation(s liveStatusSnapshot) (string, walk.Color) {
	switch s.GridState {
	case outage.StateGridDown:
		return "Grid Outage · Running off grid", walk.RGB(215, 53, 47)
	case outage.StateManualDisconnected:
		return "Manually disconnected from grid", walk.RGB(215, 53, 47)
	}
	if s.MainRelayState == outage.RelayTransition {
		return "Grid reconnecting", walk.RGB(255, 103, 24)
	}
	if s.LastError != "" {
		return "On Grid · Gateway update delayed", walk.RGB(255, 103, 24)
	}
	return "On Grid", walk.RGB(30, 180, 111)
}

func formatFreshness(at, now time.Time) string {
	if at.IsZero() || now.Sub(at) < 2*time.Second {
		return "just now"
	}
	age := now.Sub(at)
	if age < time.Minute {
		return fmt.Sprintf("%d seconds ago", int(age.Seconds()))
	}
	return fmt.Sprintf("%d minutes ago", int(age.Minutes()))
}

// pollIntervalOptions returns the supported runtime presets plus a positive
// configured interval when it is not already one of those presets.
func pollIntervalOptions(current time.Duration) []time.Duration {
	options := []time.Duration{
		1 * time.Second,
		5 * time.Second,
		10 * time.Second,
		15 * time.Second,
		30 * time.Second,
		time.Minute,
		2 * time.Minute,
		5 * time.Minute,
	}

	found := slices.Contains(options, current)
	if current > 0 && !found {
		options = append(options, current)
		slices.Sort(options)
	}

	return options
}

func formatPollInterval(interval time.Duration) string {
	if interval > 0 && interval%time.Minute == 0 {
		minutes := int(interval / time.Minute)
		if minutes == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", minutes)
	}
	if interval > 0 && interval%time.Second == 0 {
		seconds := int(interval / time.Second)
		if seconds == 1 {
			return "1 second"
		}
		return fmt.Sprintf("%d seconds", seconds)
	}
	return interval.String()
}
