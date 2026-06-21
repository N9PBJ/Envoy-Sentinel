package outage

import "testing"

func TestDetectorConfirmsOutageAndRestoration(t *testing.T) {
	d := New(2)
	if d.Observe(RelayIslanded) {
		t.Fatal("outage confirmed after only one poll")
	}
	if !d.Observe(RelayIslanded) {
		t.Fatal("outage not confirmed after two polls")
	}
	if !d.Observe(RelayTransition) {
		t.Fatal("transition should preserve confirmed outage")
	}
	if !d.Observe(RelayConnected) {
		t.Fatal("restoration confirmed after only one poll")
	}
	if d.Observe(RelayConnected) {
		t.Fatal("restoration not confirmed after two polls")
	}
}

func TestUnknownRelayBreaksConfirmationSequence(t *testing.T) {
	d := New(2)
	d.Observe(RelayIslanded)
	d.Observe(0)
	if d.Observe(RelayIslanded) {
		t.Fatal("nonconsecutive islanded readings confirmed outage")
	}
}
