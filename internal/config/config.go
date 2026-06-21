package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"
)

type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	To       string
}

const defaultPollInterval = 30 * time.Second

type Config struct {
	GatewayURL        string
	GatewaySerial     string
	EnphaseUsername   string
	EnphasePassword   string
	ReserveSOC        int
	PollInterval      time.Duration
	AllowInsecureTLS  bool
	StatePath         string
	SMTPNotifications bool
	SMTP              SMTP
	Logfile           string
	Debug             bool
}

func Load(args []string) (Config, error) {
	var cfg Config

	fs := flag.NewFlagSet("drlistener", flag.ContinueOnError)
	fs.StringVar(&cfg.GatewayURL, "gateway-url", envString("ENPHASE_GATEWAY_URL", "https://envoy.local"), "IQ Gateway base URL")
	fs.IntVar(&cfg.ReserveSOC, "reserve-soc", envInt("ENPHASE_RESERVE_SOC", -1), "configured battery reserve SOC percentage")
	fs.DurationVar(&cfg.PollInterval, "poll-interval", envDuration("DRLISTENER_POLL_INTERVAL", defaultPollInterval), "poll interval")
	fs.BoolVar(&cfg.AllowInsecureTLS, "insecure-tls", envBool("ENPHASE_INSECURE_TLS", true), "allow the gateway self-signed TLS certificate")
	fs.StringVar(&cfg.StatePath, "state-file", envString("DRLISTENER_STATE_FILE", "drlistener-state.json"), "state file path")
	fs.BoolVar(&cfg.Debug, "debug", false, "enable debug logs and save raw API responses")
	fs.BoolVar(&cfg.SMTPNotifications, "smtp-notifications", envBool("SMTP_NOTIFICATIONS_ENABLED", false), "send DR transition notifications by email")

	fs.StringVar(&cfg.SMTP.Host, "smtp-host", envString("SMTP_HOST", ""), "SMTP server host")
	fs.IntVar(&cfg.SMTP.Port, "smtp-port", envInt("SMTP_PORT", 587), "SMTP server port")
	fs.StringVar(&cfg.SMTP.Username, "smtp-user", envString("SMTP_USER", ""), "SMTP username")
	fs.StringVar(&cfg.SMTP.Password, "smtp-pass", envString("SMTP_PASS", ""), "SMTP password")
	fs.StringVar(&cfg.SMTP.From, "smtp-from", envString("SMTP_FROM", ""), "notification sender address")
	fs.StringVar(&cfg.SMTP.To, "smtp-to", envString("SMTP_TO", ""), "notification recipient address")
	fs.StringVar(&cfg.Logfile, "log-file", envString("LOGFILE", "envoy.log"), "file to log status to")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	cfg.EnphaseUsername = os.Getenv("ENPHASE_USERNAME")
	cfg.EnphasePassword = os.Getenv("ENPHASE_PASSWORD")
	cfg.GatewaySerial = os.Getenv("ENPHASE_GATEWAY_SERIAL")
	if cfg.EnphaseUsername == "" || cfg.EnphasePassword == "" || cfg.GatewaySerial == "" {
		return Config{}, errors.New("ENPHASE_USERNAME, ENPHASE_PASSWORD, and ENPHASE_GATEWAY_SERIAL are required")
	}
	if cfg.ReserveSOC < 0 || cfg.ReserveSOC > 100 {
		return Config{}, errors.New("reserve SOC is required and must be between 0 and 100; set -reserve-soc or ENPHASE_RESERVE_SOC")
	}
	if cfg.PollInterval <= 0 {
		return Config{}, errors.New("poll interval must be greater than zero")
	}
	if cfg.SMTPNotifications && (cfg.SMTP.Host == "" || cfg.SMTP.From == "" || cfg.SMTP.To == "") {
		return Config{}, errors.New("SMTP_HOST, SMTP_FROM, and SMTP_TO are required")
	}
	if cfg.SMTPNotifications && (cfg.SMTP.Port <= 0 || cfg.SMTP.Port > 65535) {
		return Config{}, fmt.Errorf("invalid SMTP port %d", cfg.SMTP.Port)
	}

	return cfg, nil
}

func envString(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
