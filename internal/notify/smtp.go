package notify

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"drlistener/internal/config"
)

type Emailer struct {
	cfg config.SMTP
}

const smtpTimeout = 30 * time.Second

func NewEmailer(cfg config.SMTP) Emailer {
	return Emailer{cfg: cfg}
}

func (e Emailer) Send(subject, body string) error {
	addr := fmt.Sprintf("%s:%d", e.cfg.Host, e.cfg.Port)
	headers := map[string]string{
		"From":         e.cfg.From,
		"To":           e.cfg.To,
		"Subject":      subject,
		"MIME-Version": "1.0",
		"Content-Type": "text/plain; charset=utf-8",
	}

	var message strings.Builder
	for key, value := range headers {
		message.WriteString(key)
		message.WriteString(": ")
		message.WriteString(value)
		message.WriteString("\r\n")
	}
	message.WriteString("\r\n")
	message.WriteString(body)
	message.WriteString("\r\n")

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	var auth smtp.Auth
	if e.cfg.Username != "" {
		auth = smtp.PlainAuth("", e.cfg.Username, e.cfg.Password, host)
	}
	if e.cfg.Port == 465 {
		return e.sendSMTPS(addr, host, auth, []byte(message.String()))
	}
	return e.sendSMTP(addr, host, auth, []byte(message.String()))
}

func (e Emailer) sendSMTP(addr, host string, auth smtp.Auth, msg []byte) error {
	conn, err := net.DialTimeout("tcp", addr, smtpTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(smtpTimeout)); err != nil {
		return err
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer func() {
		_ = client.Close()
	}()

	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(tlsConfig(host)); err != nil {
			return err
		}
	}
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	return e.sendWithClient(client, msg)
}

func (e Emailer) sendSMTPS(addr, host string, auth smtp.Auth, msg []byte) error {
	dialer := &net.Dialer{Timeout: smtpTimeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, tlsConfig(host))
	if err != nil {
		return err
	}
	defer func() {
		_ = conn.Close()
	}()
	if err := conn.SetDeadline(time.Now().Add(smtpTimeout)); err != nil {
		return err
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer func() {
		_ = client.Close()
	}()

	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	return e.sendWithClient(client, msg)
}

func tlsConfig(host string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: host,
	}
}

func (e Emailer) sendWithClient(client *smtp.Client, msg []byte) error {
	if err := client.Mail(e.cfg.From); err != nil {
		return err
	}
	if err := client.Rcpt(e.cfg.To); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(msg); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}
