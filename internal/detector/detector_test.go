package detector

import (
	"testing"
	"time"

	"drlistener/internal/gateway"
)

func TestDetectorStartsAndEndsEvent(t *testing.T) {
	start := time.Date(2026, 6, 18, 15, 0, 0, 0, time.UTC)
	d := New(DefaultConfig(20), Snapshot{})

	observations := []gateway.Sample{
		sample(start, 80, 1200, -700, 5000, 2500),
		sample(start.Add(1*time.Minute), 79.5, 1200, -700, 5000, 2500),
		sample(start.Add(2*time.Minute), 79, 1200, -700, 5000, 2500),
		sample(start.Add(10*time.Minute), 77, 1400, -900, 5000, 2500),
	}

	var result Result
	for _, observation := range observations {
		result = d.Observe(observation)
	}
	if result.Transition != Started {
		t.Fatalf("transition=%q want %q, state=%s reason=%s", result.Transition, Started, result.State, result.Reason)
	}
	d.AcknowledgeTransition(Started)

	result = d.Observe(sample(start.Add(11*time.Minute), 76, 200, 0, 5000, 2500))
	if result.State != Active {
		t.Fatalf("state=%s want active before low-discharge duration", result.State)
	}
	result = d.Observe(sample(start.Add(22*time.Minute), 76, 200, 0, 5000, 2500))
	if result.State != SuspectEnded {
		t.Fatalf("state=%s want suspected_ended", result.State)
	}
	result = d.Observe(sample(start.Add(38*time.Minute), 76, 100, 0, 5000, 2500))
	if result.Transition != Ended {
		t.Fatalf("transition=%q want ended, state=%s reason=%s", result.Transition, result.State, result.Reason)
	}
	if result.EstimatedDischargeWh <= 0 {
		t.Fatalf("EstimatedDischargeWh=%v want positive", result.EstimatedDischargeWh)
	}
}

func TestDetectorIgnoresShortDischarge(t *testing.T) {
	start := time.Date(2026, 6, 18, 15, 0, 0, 0, time.UTC)
	d := New(DefaultConfig(20), Snapshot{})

	d.Observe(sample(start, 80, 1500, -800, 5000, 2500))
	d.Observe(sample(start.Add(1*time.Minute), 79.8, 200, 0, 5000, 2500))
	result := d.Observe(sample(start.Add(2*time.Minute), 79.8, 0, 0, 5000, 2500))

	if result.Transition != NoTransition {
		t.Fatalf("transition=%q want none", result.Transition)
	}
	if result.State != Inactive {
		t.Fatalf("state=%s want inactive", result.State)
	}
}

func TestDetectorIgnoresBatteryServingACLoad(t *testing.T) {
	start := time.Date(2026, 6, 18, 15, 0, 0, 0, time.UTC)
	d := New(DefaultConfig(20), Snapshot{})

	for i := range 6 {
		result := d.Observe(sample(start.Add(time.Duration(i)*time.Minute), 80-float64(i)*0.2, 3500, 0, 0, 3500))
		if result.Transition != NoTransition {
			t.Fatalf("transition=%q want none", result.Transition)
		}
		if result.State != Inactive {
			t.Fatalf("state=%s want inactive", result.State)
		}
	}
}

func TestDetectorStartsWhenBatteryExportsPastHouseLoad(t *testing.T) {
	start := time.Date(2026, 6, 18, 15, 0, 0, 0, time.UTC)
	d := New(DefaultConfig(20), Snapshot{})

	d.Observe(sample(start, 80, 5000, -1500, 0, 3500))
	d.Observe(sample(start.Add(1*time.Minute), 79.5, 5000, -1500, 0, 3500))
	d.Observe(sample(start.Add(2*time.Minute), 79.0, 5000, -1500, 0, 3500))
	result := d.Observe(sample(start.Add(10*time.Minute), 77.0, 5000, -1500, 0, 3500))

	if result.Transition != Started {
		t.Fatalf("transition=%q want started, state=%s reason=%s", result.Transition, result.State, result.Reason)
	}
}

func TestDetectorRemainsActiveAtReserveUntilChargingResumes(t *testing.T) {
	start := time.Date(2026, 6, 18, 15, 0, 0, 0, time.UTC)
	d := New(DefaultConfig(20), Snapshot{})

	d.Observe(sample(start, 26, 1200, -700, 5000, 2500))
	d.Observe(sample(start.Add(1*time.Minute), 25.5, 1200, -700, 5000, 2500))
	d.Observe(sample(start.Add(2*time.Minute), 25, 1200, -700, 5000, 2500))
	started := d.Observe(sample(start.Add(10*time.Minute), 23.5, 1200, -700, 5000, 2500))
	if started.Transition != Started {
		t.Fatalf("transition=%q want started", started.Transition)
	}
	d.AcknowledgeTransition(Started)

	result := d.Observe(sample(start.Add(11*time.Minute), 21.0, 1200, -700, 5000, 2500))
	if result.State != Active {
		t.Fatalf("state=%s want active at reserve", result.State)
	}
	// The battery is pinned at reserve while excess solar is exported. This is
	// continuation evidence, not an event end.
	result = d.Observe(sample(start.Add(27*time.Minute), 21.0, 0, -2500, 5000, 2500))
	if result.State != Active || result.Transition != NoTransition {
		t.Fatalf("state=%s transition=%q want active with no transition", result.State, result.Transition)
	}
	result = d.Observe(sample(start.Add(60*time.Minute), 21.0, -1200, -1300, 5000, 2500))
	if result.State != Active {
		t.Fatalf("state=%s want active before sustained-charge duration", result.State)
	}
	result = d.Observe(sample(start.Add(71*time.Minute), 22.0, -1200, -1300, 5000, 2500))
	if result.State != SuspectEnded {
		t.Fatalf("state=%s want suspected_ended after charging resumes", result.State)
	}
	result = d.Observe(sample(start.Add(87*time.Minute), 25.0, -1200, -1300, 5000, 2500))
	if result.Transition != Ended {
		t.Fatalf("transition=%q want ended", result.Transition)
	}
}

func TestDetectorDoesNotEndAtReserveWithoutSolar(t *testing.T) {
	start := time.Date(2026, 6, 18, 20, 0, 0, 0, time.UTC)
	d := New(DefaultConfig(30), Snapshot{State: Active, EventStart: start, StartSOC: 80})

	for _, elapsed := range []time.Duration{0, 10 * time.Minute, 30 * time.Minute, time.Hour} {
		result := d.Observe(sample(start.Add(elapsed), 30, 0, 800, 0, 800))
		if result.State != Active || result.Transition != NoTransition {
			t.Fatalf("after %s state=%s transition=%q want active with no transition", elapsed, result.State, result.Transition)
		}
	}
}

func TestPendingTransitionSurvivesRestartUntilAcknowledged(t *testing.T) {
	start := time.Date(2026, 6, 18, 15, 0, 0, 0, time.UTC)
	d := New(DefaultConfig(20), Snapshot{})

	d.Observe(sample(start, 80, 1200, -700, 5000, 2500))
	d.Observe(sample(start.Add(time.Minute), 79.5, 1200, -700, 5000, 2500))
	d.Observe(sample(start.Add(2*time.Minute), 79, 1200, -700, 5000, 2500))
	started := d.Observe(sample(start.Add(10*time.Minute), 77, 1200, -700, 5000, 2500))
	if started.Transition != Started {
		t.Fatalf("transition=%q want started", started.Transition)
	}

	restored := New(DefaultConfig(20), d.Snapshot())
	retried := restored.Observe(sample(start.Add(11*time.Minute), 76.5, 1200, -700, 5000, 2500))
	if retried.Transition != Started || retried.EventStart != started.EventStart {
		t.Fatalf("retried transition=%q start=%s want started at %s", retried.Transition, retried.EventStart, started.EventStart)
	}
	restored.AcknowledgeTransition(Started)
	if restored.Snapshot().Pending != nil {
		t.Fatal("pending transition remains after acknowledgement")
	}
}

func TestSuspectedEndConfirmationSurvivesRestart(t *testing.T) {
	start := time.Date(2026, 6, 18, 15, 0, 0, 0, time.UTC)
	suspectedAt := start.Add(30 * time.Minute)
	restored := New(DefaultConfig(20), Snapshot{
		State:             SuspectEnded,
		EventStart:        start,
		StartSOC:          80,
		SuspectEndedAt:    suspectedAt,
		LowDischargeSince: start.Add(20 * time.Minute),
	})

	result := restored.Observe(sample(suspectedAt.Add(5*time.Minute), 70, 100, 0, 0, 1000))
	if result.State != SuspectEnded || result.Transition != NoTransition {
		t.Fatalf("state=%s transition=%q want suspected_ended without transition", result.State, result.Transition)
	}
	result = restored.Observe(sample(suspectedAt.Add(16*time.Minute), 70, 100, 0, 0, 1000))
	if result.Transition != Ended || result.EventStart != start {
		t.Fatalf("transition=%q event_start=%s want ended from %s", result.Transition, result.EventStart, start)
	}
}

func TestLegacySuspectedEndSnapshotFallsBackToActive(t *testing.T) {
	start := time.Date(2026, 6, 18, 15, 0, 0, 0, time.UTC)
	restored := New(DefaultConfig(20), Snapshot{
		State:      SuspectEnded,
		EventStart: start,
		StartSOC:   80,
	})

	if got := restored.Snapshot().State; got != Active {
		t.Fatalf("state=%s want active", got)
	}
}

func sample(at time.Time, soc, batteryW, gridW, pvW, loadW float64) gateway.Sample {
	return gateway.Sample{
		At:            at,
		SOC:           soc,
		BatteryPowerW: batteryW,
		GridPowerW:    gridW,
		PVPowerW:      pvW,
		LoadPowerW:    loadW,
	}
}
