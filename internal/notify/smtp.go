package notify

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"

	"drlistener/internal/config"
)

type Emailer struct {
	cfg config.SMTP
}

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

	if e.cfg.Username == "" {
		return smtp.SendMail(addr, nil, e.cfg.From, []string{e.cfg.To}, []byte(message.String()))
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	auth := smtp.PlainAuth("", e.cfg.Username, e.cfg.Password, host)
	if e.cfg.Port == 465 {
		return e.sendSMTPS(addr, host, auth, []byte(message.String()))
	}
	return e.sendSMTP(addr, host, auth, []byte(message.String()))
}

func (e Emailer) sendSMTP(addr, host string, auth smtp.Auth, msg []byte) error {
	client, err := smtp.Dial(addr)
	if err != nil {
		return err
	}
	defer func() {
		_ = client.Close()
	}()

	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return err
		}
	}
	if err := client.Auth(auth); err != nil {
		return err
	}
	return e.sendWithClient(client, msg)
}

func (e Emailer) sendSMTPS(addr, host string, auth smtp.Auth, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
	if err != nil {
		return err
	}
	defer func() {
		_ = conn.Close()
	}()

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer func() {
		_ = client.Close()
	}()

	if err := client.Auth(auth); err != nil {
		return err
	}
	return e.sendWithClient(client, msg)
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
