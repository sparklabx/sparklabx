package services

import (
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// Mailer sends plain-text notification mail over SMTP (STARTTLS on 587, e.g. AWS SES
// or Gmail). A nil *Mailer is valid and sends nothing, so mail stays optional.
type Mailer struct {
	addr, host, user, pass, from string
}

// NewMailer returns nil when host is empty (mail disabled).
func NewMailer(host string, port int, user, pass, from string) *Mailer {
	if host == "" || from == "" {
		return nil
	}
	return &Mailer{addr: net.JoinHostPort(host, fmt.Sprint(port)), host: host, user: user, pass: pass, from: from}
}

// SendAsync sends in the background: a slow or failing SMTP server must never
// block or fail the admin action that triggered the mail.
func (m *Mailer) SendAsync(to, subject, body string) {
	if m == nil || to == "" {
		return
	}
	go func() {
		if err := m.send(to, subject, body); err != nil {
			log.Warn().Err(err).Str("to", to).Str("subject", subject).Msg("failed to send mail")
		}
	}()
}

func (m *Mailer) send(to, subject, body string) error {
	// Header injection guard: addresses and subject come partly from user input.
	for _, v := range []string{to, subject} {
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("invalid header value")
		}
	}
	msg := strings.Join([]string{
		"From: " + m.from,
		"To: " + to,
		"Subject: " + subject,
		"Date: " + time.Now().Format(time.RFC1123Z),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}, "\r\n")
	var auth smtp.Auth
	if m.user != "" {
		auth = smtp.PlainAuth("", m.user, m.pass, m.host)
	}
	// smtp.SendMail upgrades to STARTTLS when the server offers it (required by PlainAuth).
	return smtp.SendMail(m.addr, auth, envelopeAddr(m.from), []string{to}, []byte(msg))
}

// envelopeAddr extracts "a@b" from "Name <a@b>".
func envelopeAddr(from string) string {
	if i, j := strings.LastIndex(from, "<"), strings.LastIndex(from, ">"); i >= 0 && j > i {
		return from[i+1 : j]
	}
	return from
}
