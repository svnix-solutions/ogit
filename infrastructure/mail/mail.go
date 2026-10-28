// Package mail sends plain-text email over SMTP.
package mail

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/labbs/ogit/infrastructure/config"
)

// Sender sends an email. The SMTP implementation is used in production;
// tests substitute a recorder.
type Sender interface {
	Send(to, subject, body string) error
}

// SMTPSender sends mail through the smtp.* settings.
type SMTPSender struct {
	Config  config.Config
	Timeout time.Duration
}

func NewSMTPSender(cfg config.Config) *SMTPSender {
	return &SMTPSender{Config: cfg, Timeout: 15 * time.Second}
}

func (s *SMTPSender) Send(to, subject, body string) error {
	cfg := s.Config.SMTP
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return fmt.Errorf("invalid smtp.from: %w", err)
	}
	rcpt, err := mail.ParseAddress(to)
	if err != nil {
		return fmt.Errorf("invalid recipient: %w", err)
	}
	msg, err := buildMessage(from, rcpt, subject, body, time.Now())
	if err != nil {
		return err
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := &net.Dialer{Timeout: s.Timeout}
	tlsConfig := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	if cfg.TLS == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(s.Timeout))

	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp handshake: %w", err)
	}
	defer c.Close()

	if cfg.TLS == "starttls" {
		if err := c.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if cfg.Username != "" {
		// PlainAuth refuses to send credentials without TLS, except to localhost.
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err := c.Rcpt(rcpt.Address); err != nil {
		return fmt.Errorf("smtp RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	return c.Quit()
}

// buildMessage renders a plain-text RFC 5322 message with CRLF line endings.
func buildMessage(from, to *mail.Address, subject, body string, now time.Time) ([]byte, error) {
	if strings.ContainsAny(subject, "\r\n") {
		return nil, errors.New("subject must not contain line breaks")
	}

	id := make([]byte, 12)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	domain := from.Address[strings.LastIndex(from.Address, "@")+1:]

	var b strings.Builder
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", from.String())
	header("To", to.String())
	header("Subject", mimeHeader(subject))
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=UTF-8")
	header("Content-Transfer-Encoding", "8bit")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))
	return []byte(b.String()), nil
}

// mimeHeader encodes non-ASCII header values (RFC 2047).
func mimeHeader(s string) string {
	for _, r := range s {
		if r > 127 {
			return mime.QEncoding.Encode("UTF-8", s)
		}
	}
	return s
}
