package main

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"drlistener/internal/detector"
	"drlistener/internal/gateway"
	"drlistener/internal/state"
)

type stubEmailSender struct {
	err error
}

func (s stubEmailSender) Send(string, string) error {
	return s.err
}

func TestNotifyTransitionRemainsPendingUntilDeliverySucceeds(t *testing.T) {
	det, result := startedTransition(t)

	path := filepath.Join(t.TempDir(), "state.json")
	if err := state.Save(path, det.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := notifyTransition(det, stubEmailSender{err: errors.New("SMTP unavailable")}, path, result); err == nil {
		t.Fatal("failed delivery returned nil error")
	}
	if det.Snapshot().Pending == nil {
		t.Fatal("failed delivery cleared pending transition")
	}

	if err := notifyTransition(det, stubEmailSender{}, path, result); err != nil {
		t.Fatal(err)
	}
	if det.Snapshot().Pending != nil {
		t.Fatal("successful delivery did not clear pending transition")
	}
	saved, err := state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Pending != nil {
		t.Fatal("acknowledged transition remains pending on disk")
	}
}

func TestNotifyTransitionAcknowledgesWithoutEmailWhenDisabled(t *testing.T) {
	det, result := startedTransition(t)
	path := filepath.Join(t.TempDir(), "state.json")

	if err := notifyTransition(det, nil, path, result); err != nil {
		t.Fatal(err)
	}
	if det.Snapshot().Pending != nil {
		t.Fatal("disabled notification remains pending")
	}
}

func startedTransition(t *testing.T) (*detector.Detector, detector.Result) {
	t.Helper()
	start := time.Date(2026, 6, 18, 15, 0, 0, 0, time.UTC)
	det := detector.New(detector.DefaultConfig(20), detector.Snapshot{})
	samples := []gateway.Sample{
		{At: start, SOC: 80, BatteryPowerW: 1200, GridPowerW: -700},
		{At: start.Add(time.Minute), SOC: 79.5, BatteryPowerW: 1200, GridPowerW: -700},
		{At: start.Add(2 * time.Minute), SOC: 79, BatteryPowerW: 1200, GridPowerW: -700},
		{At: start.Add(10 * time.Minute), SOC: 77, BatteryPowerW: 1200, GridPowerW: -700},
	}
	var result detector.Result
	for _, sample := range samples {
		result = det.Observe(sample)
	}
	if result.Transition != detector.Started {
		t.Fatalf("transition=%q want started", result.Transition)
	}
	return det, result
}
