package detector

import (
	"fmt"
	"time"

	"drlistener/internal/gateway"
)

// State describes the detector's current position in the DR-event state
// machine. Suspect states provide hysteresis so a single noisy sample cannot
// send a notification.
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
	pending              *Result
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
	State                State     `json:"state"`
	EventStart           time.Time `json:"event_start,omitempty"`
	StartSOC             float64   `json:"start_soc,omitempty"`
	SuspectActiveAt      time.Time `json:"suspect_active_at,omitempty"`
	SuspectActiveSOC     float64   `json:"suspect_active_soc,omitempty"`
	SuspectEndedAt       time.Time `json:"suspect_ended_at,omitempty"`
	LowDischargeSince    time.Time `json:"low_discharge_since,omitempty"`
	EstimatedDischargeWh float64   `json:"estimated_discharge_wh,omitempty"`
	Pending              *Result   `json:"pending_notification,omitempty"`
}

func DefaultConfig(reserveSOC int) Config {
	// These are intentionally conservative heuristics, not values supplied by
	// Enphase. See README.md for the evidence and timing behind each threshold.
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
	switch state {
	case Inactive, SuspectActive, Active, SuspectEnded:
	default:
		state = Inactive
	}
	// Snapshots written by older versions did not contain provisional-state
	// timestamps. Fall back safely instead of treating a zero timestamp as an
	// immediately confirmed transition.
	if state == SuspectActive && snapshot.SuspectActiveAt.IsZero() {
		state = Inactive
	}
	if state == SuspectEnded && snapshot.SuspectEndedAt.IsZero() {
		state = Active
	}
	d := &Detector{
		cfg:                  cfg,
		state:                state,
		suspectActiveAt:      snapshot.SuspectActiveAt,
		suspectActiveSOC:     snapshot.SuspectActiveSOC,
		suspectEndedAt:       snapshot.SuspectEndedAt,
		lowDischargeSince:    snapshot.LowDischargeSince,
		estimatedDischargeWh: snapshot.EstimatedDischargeWh,
		pending:              snapshot.Pending,
	}
	if (state == Active || state == SuspectEnded) && !snapshot.EventStart.IsZero() {
		d.activeEventStart = snapshot.EventStart
		d.activeEventStartSOC = snapshot.StartSOC
	}
	return d
}

func (d *Detector) Snapshot() Snapshot {
	s := Snapshot{
		State:                d.state,
		SuspectActiveAt:      d.suspectActiveAt,
		SuspectActiveSOC:     d.suspectActiveSOC,
		SuspectEndedAt:       d.suspectEndedAt,
		LowDischargeSince:    d.lowDischargeSince,
		EstimatedDischargeWh: d.estimatedDischargeWh,
		Pending:              d.pending,
	}
	if d.state == Active || d.state == SuspectEnded {
		s.EventStart = d.activeEventStart
		s.StartSOC = d.activeEventStartSOC
	}
	return s
}

// AcknowledgeTransition clears a pending transition after its notification is
// delivered. Until acknowledged, Observe returns the same transition and
// pauses state-machine transitions so a temporary SMTP failure cannot lose or
// reorder notifications.
func (d *Detector) AcknowledgeTransition(transition Transition) {
	if d.pending != nil && d.pending.Transition == transition {
		d.pending = nil
	}
}

func (d *Detector) Observe(sample gateway.Sample) Result {
	d.addDischargeEnergy(sample)
	d.lastSample = sample
	d.hasLastSample = true
	d.addHistory(sample)
	if d.pending != nil {
		return *d.pending
	}

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
			d.estimatedDischargeWh = d.recentDischargeEnergy(d.activeEventStart, sample.At)
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
			result.Reason = d.endEvidenceReason(sample)
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
	if result.Transition != NoTransition {
		pending := result
		d.pending = &pending
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
	// Once a confirmed event reaches reserve, an idle battery is not evidence
	// that the event ended. A DR command can keep the battery pinned there and
	// force surplus PV to the grid. At reserve, wait for positive evidence that
	// normal control has returned: sustained battery charging. Above reserve,
	// the original sustained-low-discharge rule remains useful.
	endEvidence := sample.BatteryPowerW < d.cfg.EndDischargeW
	if atReserve {
		endEvidence = sample.BatteryPowerW <= -d.cfg.EndDischargeW
	}

	if endEvidence {
		if d.lowDischargeSince.IsZero() {
			d.lowDischargeSince = sample.At
		}
	} else {
		d.lowDischargeSince = time.Time{}
	}

	return !d.lowDischargeSince.IsZero() && sample.At.Sub(d.lowDischargeSince) >= d.cfg.LowDischargeFor
}

func (d *Detector) endEvidenceReason(sample gateway.Sample) string {
	if sample.SOC <= d.cfg.ReserveSOC+d.cfg.ReserveMarginSOC {
		return "battery resumed sustained charging at reserve"
	}
	return "battery discharge remained below end threshold"
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
	// A gap over an hour likely means the process or gateway was unavailable;
	// integrating across it would invent energy from an unobserved interval.
	if hours <= 0 || hours > 1 {
		return
	}
	d.estimatedDischargeWh += sample.BatteryPowerW * hours
}

// recentDischargeEnergy accounts for the confirmation window that precedes a
// Started transition. Without it, an event backdated to suspectActiveAt would
// report no energy for its first several minutes.
func (d *Detector) recentDischargeEnergy(start, end time.Time) float64 {
	var wattHours float64
	for i := 1; i < len(d.history); i++ {
		previous := d.history[i-1]
		current := d.history[i]
		if current.At.Before(start) || current.At.After(end) || current.BatteryPowerW <= 0 {
			continue
		}
		hours := current.At.Sub(previous.At).Hours()
		if hours > 0 && hours <= 1 {
			wattHours += current.BatteryPowerW * hours
		}
	}
	return wattHours
}
