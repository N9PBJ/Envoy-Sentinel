package detector

import (
	"fmt"
	"time"

	"drlistener/internal/gateway"
)

type State string

const (
	Inactive      State = "inactive"
	SuspectActive State = "suspected_active"
	Active        State = "active"
	SuspectEnded  State = "suspected_ended"
)

type Config struct {
	ReserveSOC             float64
	StartDischargeW        float64
	EndDischargeW          float64
	GridExportW            float64
	SurplusDischargeW      float64
	ReserveMarginSOC       float64
	ConfirmSOCDrop         float64
	ActiveConsecutivePolls int
	ConfirmActiveWithin    time.Duration
	LowDischargeFor        time.Duration
	ConfirmEndedAfter      time.Duration
	HistoryWindow          time.Duration
}

type Detector struct {
	cfg                  Config
	state                State
	history              []gateway.Sample
	sustainedDischarge   int
	suspectActiveAt      time.Time
	suspectActiveSOC     float64
	suspectEndedAt       time.Time
	lowDischargeSince    time.Time
	activeEventStart     time.Time
	activeEventStartSOC  float64
	lastSample           gateway.Sample
	hasLastSample        bool
	estimatedDischargeWh float64
}

type Transition string

const (
	NoTransition Transition = ""
	Started      Transition = "started"
	Ended        Transition = "ended"
)

type Result struct {
	State                State
	Transition           Transition
	Reason               string
	Sample               gateway.Sample
	EventStart           time.Time
	EventEnd             time.Time
	StartSOC             float64
	EndSOC               float64
	EstimatedDischargeWh float64
}

type Snapshot struct {
	State      State     `json:"state"`
	EventStart time.Time `json:"event_start,omitempty"`
	StartSOC   float64   `json:"start_soc,omitempty"`
}

func DefaultConfig(reserveSOC int) Config {
	return Config{
		ReserveSOC:             float64(reserveSOC),
		StartDischargeW:        1000,
		EndDischargeW:          300,
		GridExportW:            500,
		SurplusDischargeW:      750,
		ReserveMarginSOC:       2,
		ConfirmSOCDrop:         2,
		ActiveConsecutivePolls: 3,
		ConfirmActiveWithin:    20 * time.Minute,
		LowDischargeFor:        10 * time.Minute,
		ConfirmEndedAfter:      15 * time.Minute,
		HistoryWindow:          30 * time.Minute,
	}
}

func New(cfg Config, snapshot Snapshot) *Detector {
	state := snapshot.State
	if state == "" {
		state = Inactive
	}
	d := &Detector{cfg: cfg, state: state}
	if state == Active && !snapshot.EventStart.IsZero() {
		d.activeEventStart = snapshot.EventStart
		d.activeEventStartSOC = snapshot.StartSOC
	}
	return d
}

func (d *Detector) Snapshot() Snapshot {
	s := Snapshot{State: d.state}
	if d.state == Active || d.state == SuspectEnded {
		s.EventStart = d.activeEventStart
		s.StartSOC = d.activeEventStartSOC
	}
	return s
}

func (d *Detector) Observe(sample gateway.Sample) Result {
	d.addDischargeEnergy(sample)
	d.lastSample = sample
	d.hasLastSample = true
	d.addHistory(sample)

	result := Result{State: d.state, Sample: sample}
	if d.isEventLikeDischarge(sample) {
		d.sustainedDischarge++
	} else {
		d.sustainedDischarge = 0
	}

	switch d.state {
	case Inactive:
		if d.sustainedDischarge >= d.cfg.ActiveConsecutivePolls {
			d.state = SuspectActive
			d.suspectActiveAt = sample.At
			d.suspectActiveSOC = d.highestRecentSOC(sample.At.Add(-d.cfg.ConfirmActiveWithin), sample.At)
			result.State = d.state
			result.Reason = d.eventLikeReason(sample)
		}
	case SuspectActive:
		if !d.isEventLikeDischarge(sample) {
			d.state = Inactive
			result.State = d.state
			result.Reason = "event-like discharge stopped before DR confirmation"
			break
		}
		drop := d.suspectActiveSOC - sample.SOC
		withinWindow := sample.At.Sub(d.suspectActiveAt) <= d.cfg.ConfirmActiveWithin
		if drop >= d.cfg.ConfirmSOCDrop && withinWindow {
			d.state = Active
			d.activeEventStart = d.suspectActiveAt
			d.activeEventStartSOC = d.suspectActiveSOC
			d.estimatedDischargeWh = 0
			result.State = d.state
			result.Transition = Started
			result.EventStart = d.activeEventStart
			result.StartSOC = d.activeEventStartSOC
			result.Reason = fmt.Sprintf("SOC dropped %.1f%% while battery discharge remained sustained", drop)
		}
		if !withinWindow {
			d.state = Inactive
			result.State = d.state
			result.Reason = "SOC did not drop enough within confirmation window"
		}
	case Active:
		if d.shouldSuspectEnded(sample) {
			d.state = SuspectEnded
			d.suspectEndedAt = sample.At
			result.State = d.state
			result.Reason = "battery discharge fell below end threshold or SOC reached reserve"
		}
	case SuspectEnded:
		if d.isEventLikeDischarge(sample) {
			d.state = Active
			d.suspectEndedAt = time.Time{}
			result.State = d.state
			result.Reason = "event-like discharge resumed before DR end confirmation"
			break
		}
		if sample.At.Sub(d.suspectEndedAt) >= d.cfg.ConfirmEndedAfter {
			result.State = Inactive
			result.Transition = Ended
			result.EventStart = d.activeEventStart
			result.EventEnd = sample.At
			result.StartSOC = d.activeEventStartSOC
			result.EndSOC = sample.SOC
			result.EstimatedDischargeWh = d.estimatedDischargeWh
			result.Reason = "battery discharge remained below end threshold"

			d.state = Inactive
			d.activeEventStart = time.Time{}
			d.activeEventStartSOC = 0
			d.estimatedDischargeWh = 0
			d.suspectEndedAt = time.Time{}
		}
	}

	if result.State == "" {
		result.State = d.state
	}
	return result
}

func (d *Detector) isSustainedDischarge(sample gateway.Sample) bool {
	return sample.BatteryPowerW >= d.cfg.StartDischargeW
}

func (d *Detector) isEventLikeDischarge(sample gateway.Sample) bool {
	if !d.isSustainedDischarge(sample) {
		return false
	}
	if sample.SOC <= d.cfg.ReserveSOC+d.cfg.ReserveMarginSOC {
		return false
	}
	if sample.GridPowerW <= -d.cfg.GridExportW {
		return true
	}
	if sample.LoadPowerW > 0 {
		netHouseDemandAfterPV := sample.LoadPowerW - sample.PVPowerW
		if netHouseDemandAfterPV < 0 {
			netHouseDemandAfterPV = 0
		}
		return sample.BatteryPowerW-netHouseDemandAfterPV >= d.cfg.SurplusDischargeW
	}
	return false
}

func (d *Detector) eventLikeReason(sample gateway.Sample) string {
	if sample.GridPowerW <= -d.cfg.GridExportW {
		return fmt.Sprintf("sustained battery discharge %.0f W while exporting %.0f W to grid", sample.BatteryPowerW, -sample.GridPowerW)
	}
	netHouseDemandAfterPV := sample.LoadPowerW - sample.PVPowerW
	if netHouseDemandAfterPV < 0 {
		netHouseDemandAfterPV = 0
	}
	return fmt.Sprintf("sustained battery discharge %.0f W exceeds net house demand %.0f W", sample.BatteryPowerW, netHouseDemandAfterPV)
}

func (d *Detector) shouldSuspectEnded(sample gateway.Sample) bool {
	atReserve := sample.SOC <= d.cfg.ReserveSOC+d.cfg.ReserveMarginSOC
	lowDischarge := sample.BatteryPowerW < d.cfg.EndDischargeW

	if lowDischarge {
		if d.lowDischargeSince.IsZero() {
			d.lowDischargeSince = sample.At
		}
	} else {
		d.lowDischargeSince = time.Time{}
	}

	lowLongEnough := !d.lowDischargeSince.IsZero() && sample.At.Sub(d.lowDischargeSince) >= d.cfg.LowDischargeFor
	return atReserve || lowLongEnough
}

func (d *Detector) addHistory(sample gateway.Sample) {
	d.history = append(d.history, sample)
	cutoff := sample.At.Add(-d.cfg.HistoryWindow)
	keep := 0
	for _, item := range d.history {
		if !item.At.Before(cutoff) {
			d.history[keep] = item
			keep++
		}
	}
	d.history = d.history[:keep]
}

func (d *Detector) highestRecentSOC(start, end time.Time) float64 {
	highest := 0.0
	for _, item := range d.history {
		if item.At.Before(start) || item.At.After(end) {
			continue
		}
		if item.SOC > highest {
			highest = item.SOC
		}
	}
	return highest
}

func (d *Detector) addDischargeEnergy(sample gateway.Sample) {
	if d.state != Active && d.state != SuspectEnded {
		return
	}
	if !d.hasLastSample || sample.At.Before(d.lastSample.At) || sample.BatteryPowerW <= 0 {
		return
	}
	hours := sample.At.Sub(d.lastSample.At).Hours()
	if hours <= 0 || hours > 1 {
		return
	}
	d.estimatedDischargeWh += sample.BatteryPowerW * hours
}
