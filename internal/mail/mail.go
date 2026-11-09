// Package mail builds multipart/alternative messages and sends them over
// SMTP (STARTTLS, implicit TLS, or plain for a local relay).
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

type Message struct {
	From    string   // "Radar <radar@example.com>" or bare address
	To      []string // addresses
	Subject string
	Text    string
	HTML    string
	Date    time.Time
}

// Envelope returns the bare SMTP sender and recipient addresses.
func (m Message) Envelope() (from string, to []string, err error) {
	f, err := netmail.ParseAddress(m.From)
	if err != nil {
		return "", nil, fmt.Errorf("from %q: %w", m.From, err)
	}
	for _, r := range m.To {
		a, err := netmail.ParseAddress(r)
		if err != nil {
			return "", nil, fmt.Errorf("to %q: %w", r, err)
		}
		to = append(to, a.Address)
	}
	if len(to) == 0 {
		return "", nil, errors.New("no recipients")
	}
	return f.Address, to, nil
}

// Bytes renders the message as RFC 5322 with a multipart/alternative body
// (text first, HTML preferred), both quoted-printable UTF-8.
func (m Message) Bytes() ([]byte, error) {
	from, err := netmail.ParseAddress(m.From)
	if err != nil {
		return nil, fmt.Errorf("from %q: %w", m.From, err)
	}
	var to []string
	for _, r := range m.To {
		a, err := netmail.ParseAddress(r)
		if err != nil {
			return nil, fmt.Errorf("to %q: %w", r, err)
		}
		to = append(to, a.String())
	}
	date := m.Date
	if date.IsZero() {
		date = time.Now()
	}
	domain := "radar.local"
	if i := strings.LastIndexByte(from.Address, '@'); i >= 0 {
		domain = from.Address[i+1:]
	}
	rnd := make([]byte, 12)
	rand.Read(rnd)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, part := range []struct{ ctype, content string }{
		{"text/plain; charset=utf-8", m.Text},
		{"text/html; charset=utf-8", m.HTML},
	} {
		if part.content == "" {
			continue
		}
		w, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.ctype},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(w)
		if _, err := qp.Write([]byte(part.content)); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	var out bytes.Buffer
	for _, h := range [][2]string{
		{"From", from.String()},
		{"To", strings.Join(to, ", ")},
		{"Subject", mime.QEncoding.Encode("utf-8", m.Subject)},
		{"Date", date.Format(time.RFC1123Z)},
		{"Message-ID", "<" + hex.EncodeToString(rnd) + "@" + domain + ">"},
		{"MIME-Version", "1.0"},
		{"Content-Type", `multipart/alternative; boundary="` + mw.Boundary() + `"`},
		{"Auto-Submitted", "auto-generated"},
	} {
		out.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	out.WriteString("\r\n")
	out.Write(body.Bytes())
	return out.Bytes(), nil
}

// TLSMode selects how the SMTP connection is secured.
type TLSMode string

const (
	StartTLS TLSMode = "starttls" // plain connect, then STARTTLS (required); usually port 587
	Implicit TLSMode = "tls"      // TLS from the first byte; usually port 465
	NoTLS    TLSMode = "none"     // local relay only; auth is refused off-localhost by net/smtp
)

type SMTP struct {
	Host     string
	Port     int
	TLS      TLSMode
	Username string
	Password string
	Timeout  time.Duration // whole-session deadline; default 1 minute
	// TLSConfig overrides the TLS settings (tests). ServerName defaults to Host.
	TLSConfig *tls.Config
}

// Send delivers m.
func (s *SMTP) Send(ctx context.Context, m Message) error {
	from, to, err := m.Envelope()
	if err != nil {
		return err
	}
	raw, err := m.Bytes()
	if err != nil {
		return err
	}
	return s.send(ctx, from, to, raw)
}

func (s *SMTP) send(ctx context.Context, from string, to []string, raw []byte) error {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tlsCfg := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	if s.TLSConfig != nil {
		tlsCfg = s.TLSConfig.Clone()
		if tlsCfg.ServerName == "" {
			tlsCfg.ServerName = s.Host
		}
	}

	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	var (
		conn net.Conn
		err  error
	)
	if s.TLS == Implicit {
		conn, err = (&tls.Dialer{Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("smtp dial %s: %w", addr, err)
	}
	return s.session(ctx, conn, tlsCfg, from, to, raw)
}

func (s *SMTP) session(ctx context.Context, conn net.Conn, tlsCfg *tls.Config, from string, to []string, raw []byte) error {
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	// Unblock any in-flight read/write if ctx is cancelled early.
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	defer stop()

	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp greeting: %w", err)
	}
	defer c.Close()

	if s.TLS == StartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("smtp: server does not offer STARTTLS (use tls: tls for port 465)")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if s.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("smtp: server does not offer AUTH")
		}
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	for _, r := range to {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("smtp RCPT TO %s: %w", r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp end of data: %w", err)
	}
	return c.Quit()
}
