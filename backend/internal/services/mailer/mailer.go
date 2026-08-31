// Package mailer sends the few messages the instance produces.
//
// It speaks SMTP directly rather than pulling in a dependency: the traffic is
// a handful of notifications, and an operator configuring a mailbox wants the
// failure to name the step that failed, not a library's wrapper around it.
package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Errors an operator can act on.
var (
	ErrNotConfigured = errors.New("mailer: outgoing mail is not configured")
	ErrNoRecipient   = errors.New("mailer: no recipient given")
)

// Settings are the parts of the platform configuration a send needs.
type Settings struct {
	Enabled  bool
	Host     string
	Port     int
	Security string
	Username string
	Password string
	From     string
	Timeout  time.Duration
}

// Security modes, matching what the settings page offers.
const (
	SecuritySSL      = "ssl"
	SecurityStartTLS = "starttls"
	SecurityNone     = "none"
)

// Message is one outgoing mail.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Send delivers one message.
//
// Every step is reported with the phase it failed in — "dial", "auth", "send" —
// because "connection refused" alone tells an operator nothing about whether
// the host, the port or the credentials are wrong.
func Send(ctx context.Context, settings Settings, message Message) error {
	if !settings.Enabled || strings.TrimSpace(settings.Host) == "" ||
		strings.TrimSpace(settings.From) == "" {
		return ErrNotConfigured
	}
	if strings.TrimSpace(message.To) == "" {
		return ErrNoRecipient
	}

	timeout := settings.Timeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	address := net.JoinHostPort(settings.Host, fmt.Sprint(settings.Port))
	dialer := &net.Dialer{Timeout: timeout}

	var connection net.Conn
	var err error
	if settings.Security == SecuritySSL {
		connection, err = tls.DialWithDialer(dialer, "tcp", address,
			&tls.Config{ServerName: settings.Host, MinVersion: tls.VersionTLS12})
	} else {
		connection, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return fmt.Errorf("dial %s: %w", address, err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(timeout))

	client, err := smtp.NewClient(connection, settings.Host)
	if err != nil {
		return fmt.Errorf("greet %s: %w", settings.Host, err)
	}
	defer client.Close()

	if settings.Security == SecurityStartTLS {
		if err := client.StartTLS(&tls.Config{
			ServerName: settings.Host, MinVersion: tls.VersionTLS12,
		}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}

	if settings.Username != "" {
		auth := smtp.PlainAuth("", settings.Username, settings.Password, settings.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("auth as %s: %w", settings.Username, err)
		}
	}

	if err := client.Mail(settings.From); err != nil {
		return fmt.Errorf("sender %s rejected: %w", settings.From, err)
	}
	if err := client.Rcpt(message.To); err != nil {
		return fmt.Errorf("recipient %s rejected: %w", message.To, err)
	}

	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	if _, err := writer.Write(compose(settings.From, message)); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	return client.Quit()
}

// compose builds a minimal RFC 5322 message. The subject is encoded because
// the band names and reasons that end up in one are rarely ASCII.
func compose(from string, message Message) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + message.To + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", message.Subject) + "\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	// A bare dot would end the data phase early.
	b.WriteString(strings.ReplaceAll(message.Body, "\r\n.", "\r\n.."))
	b.WriteString("\r\n")
	return []byte(b.String())
}
