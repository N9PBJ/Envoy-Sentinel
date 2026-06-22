//go:build windows

package main

import (
	"drlistener/internal/detector"
	"drlistener/internal/outage"
	"testing"
	"time"
)

func TestFormatCountdownRoundsUp(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{15 * time.Minute, "15:00"},
		{14*time.Minute + 59*time.Second + time.Millisecond, "15:00"},
		{time.Millisecond, "00:01"},
		{-time.Second, "00:00"},
	}
	for _, test := range tests {
		if got := formatCountdown(test.duration); got != test.want {
			t.Errorf("formatCountdown(%v)=%q want %q", test.duration, got, test.want)
		}
	}
}

func TestGridPresentation(t *testing.T) {
	tests := []struct {
		name        string
		state       outage.State
		relay       int
		watts       float64
		wantValue   string
		wantCaption string
	}{
		{"export", outage.StateConnected, outage.RelayConnected, -4500, "4.5 kW", "Exporting"},
		{"import", outage.StateConnected, outage.RelayConnected, 1200, "1.2 kW", "Importing"},
		{"outage", outage.StateGridDown, outage.RelayGridDown, 0, "OFF GRID", "Islanded"},
		{"transition", outage.StateConnected, outage.RelayTransition, 0, "-- kW", "Reconnecting"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, caption := gridPresentation(test.state, test.relay, test.watts)
			if value != test.wantValue || caption != test.wantCaption {
				t.Fatalf("gridPresentation()=(%q,%q) want (%q,%q)", value, caption, test.wantValue, test.wantCaption)
			}
		})
	}
}

func TestBatteryPresentationUsesGatewaySignConvention(t *testing.T) {
	value, caption := batteryPresentation(3100)
	if value != "3.1 kW" || caption != "Discharging" {
		t.Fatalf("positive battery=(%q,%q)", value, caption)
	}
	value, caption = batteryPresentation(-2400)
	if value != "2.4 kW" || caption != "Charging" {
		t.Fatalf("negative battery=(%q,%q)", value, caption)
	}
}

func TestProfileName(t *testing.T) {
	if got := profileName(detector.Inactive); got != "Self-Consumption" {
		t.Fatalf("inactive profile=%q", got)
	}
	if got := profileName(detector.Active); got != "Demand Response" {
		t.Fatalf("active profile=%q", got)
	}
}

func TestPollIntervalOptionsIncludeConfiguredValue(t *testing.T) {
	configured := 45 * time.Second
	options := pollIntervalOptions(configured)
	for _, option := range options {
		if option == configured {
			return
		}
	}
	t.Fatalf("options %v do not include configured interval %v", options, configured)
}

func TestFormatPollInterval(t *testing.T) {
	tests := map[time.Duration]string{
		5 * time.Second: "5 seconds",
		time.Minute:     "1 minute",
		2 * time.Minute: "2 minutes",
	}
	for interval, want := range tests {
		if got := formatPollInterval(interval); got != want {
			t.Errorf("formatPollInterval(%v)=%q want %q", interval, got, want)
		}
	}
}

func TestFormatFreshnessUsesSingularMinute(t *testing.T) {
	now := time.Date(2026, 6, 22, 14, 0, 0, 0, time.UTC)
	if got := formatFreshness(now.Add(-time.Minute), now); got != "1 minute ago" {
		t.Fatalf("formatFreshness()=%q want %q", got, "1 minute ago")
	}
}
