package outage

const (
	// Relay values come from meters.main_relay_state in the IQ Gateway live-data
	// response. Unknown values are intentionally handled like RelayTransition.
	RelayConnected  = 1
	RelayIslanded   = 2
	RelayTransition = 3
)

type Detector struct {
	confirmPolls   int
	islandedPolls  int
	connectedPolls int
	islanded       bool
}

// New returns a relay-state debouncer. confirmPolls is the number of
// consecutive connected or islanded readings required to change state.
func New(confirmPolls int) *Detector {
	if confirmPolls < 1 {
		confirmPolls = 1
	}
	return &Detector{confirmPolls: confirmPolls}
}

// Observe requires consecutive stable relay readings. Transitional or unknown
// values preserve the last confirmed state but reset confirmation counters.
func (d *Detector) Observe(mainRelayState int) bool {
	switch mainRelayState {
	case RelayIslanded:
		d.islandedPolls++
		d.connectedPolls = 0
		if d.islandedPolls >= d.confirmPolls {
			d.islanded = true
		}
	case RelayConnected:
		d.connectedPolls++
		d.islandedPolls = 0
		if d.connectedPolls >= d.confirmPolls {
			d.islanded = false
		}
	default:
		d.islandedPolls = 0
		d.connectedPolls = 0
	}
	return d.islanded
}
