package config

import (
	"strings"
	"testing"
)

func TestSMTPNotificationsDefaultToDisabled(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("SMTP_NOTIFICATIONS_ENABLED", "")
	t.Setenv("SMTP_HOST", "")
	t.Setenv("SMTP_FROM", "")
	t.Setenv("SMTP_TO", "")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SMTPNotifications {
		t.Fatal("SMTP notifications enabled by default")
	}
}

func TestSMTPSettingsRequiredWhenNotificationsEnabled(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("SMTP_NOTIFICATIONS_ENABLED", "true")
	t.Setenv("SMTP_HOST", "")
	t.Setenv("SMTP_FROM", "")
	t.Setenv("SMTP_TO", "")

	_, err := Load(nil)
	if err == nil || !strings.Contains(err.Error(), "SMTP_HOST") {
		t.Fatalf("error=%v want missing SMTP configuration", err)
	}
}

func setRequiredEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("ENPHASE_USERNAME", "owner@example.com")
	t.Setenv("ENPHASE_PASSWORD", "secret")
	t.Setenv("ENPHASE_GATEWAY_SERIAL", "serial")
	t.Setenv("ENPHASE_RESERVE_SOC", "20")
}
