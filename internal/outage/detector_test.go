package outage

import "testing"

func TestDetectorClassifiesStableRelayStates(t *testing.T) {
	d := New(2)
	tests := []struct {
		relay int
		want  State
	}{
		{RelayGridDown, StateUnknown},
		{RelayGridDown, StateGridDown},
		{RelayManualDisconnected, StateGridDown},
		{RelayManualDisconnected, StateManualDisconnected},
		{RelayConnected, StateManualDisconnected},
		{RelayConnected, StateConnected},
	}
	for i, tt := range tests {
		if got := d.Observe(tt.relay); got != tt.want {
			t.Fatalf("step %d: Observe(%d)=%q want %q", i, tt.relay, got, tt.want)
		}
	}
}

func TestTransitionAndUnknownPreserveConfirmedState(t *testing.T) {
	d := New(2)
	d.Observe(RelayGridDown)
	d.Observe(RelayGridDown)
	if got := d.Observe(RelayTransition); got != StateGridDown {
		t.Fatalf("transition changed state to %q", got)
	}
	if got := d.Observe(99); got != StateGridDown {
		t.Fatalf("unknown relay changed state to %q", got)
	}
}

func TestTransitionBreaksConfirmationSequence(t *testing.T) {
	d := New(2)
	d.Observe(RelayManualDisconnected)
	d.Observe(RelayTransition)
	if got := d.Observe(RelayManualDisconnected); got != StateUnknown {
		t.Fatalf("nonconsecutive readings confirmed %q", got)
	}
}
