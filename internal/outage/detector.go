package outage

type State string

const (
	StateUnknown            State = "unknown"
	StateConnected          State = "connected"
	StateGridDown           State = "grid_down"
	StateManualDisconnected State = "manual_disconnected"

	RelayGridDown           = 0
	RelayConnected          = 1
	RelayManualDisconnected = 2
	RelayTransition         = 3
)

type Detector struct {
	confirmPolls int
	candidate    State
	candidateN   int
	state        State
}

// New returns a relay-state debouncer. confirmPolls is the number of
// consecutive readings required to confirm a stable state change.
func New(confirmPolls int) *Detector {
	if confirmPolls < 1 {
		confirmPolls = 1
	}
	return &Detector{confirmPolls: confirmPolls, state: StateUnknown}
}

// Observe requires consecutive stable relay readings. Transitional or unknown
// values preserve the last confirmed state but reset the confirmation sequence.
func (d *Detector) Observe(mainRelayState int) State {
	next, stable := relayState(mainRelayState)
	if !stable {
		d.candidate = StateUnknown
		d.candidateN = 0
		return d.state
	}
	if next == d.state {
		d.candidate = StateUnknown
		d.candidateN = 0
		return d.state
	}
	if next != d.candidate {
		d.candidate = next
		d.candidateN = 1
	} else {
		d.candidateN++
	}
	if d.candidateN >= d.confirmPolls {
		d.state = next
		d.candidate = StateUnknown
		d.candidateN = 0
	}
	return d.state
}

func relayState(mainRelayState int) (State, bool) {
	switch mainRelayState {
	case RelayGridDown:
		return StateGridDown, true
	case RelayConnected:
		return StateConnected, true
	case RelayManualDisconnected:
		return StateManualDisconnected, true
	default:
		return StateUnknown, false
	}
}
