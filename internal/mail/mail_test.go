package mail

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"net"
	netmail "net/mail"
	"strings"
	"testing"
	"time"
)

func TestMessageBytes(t *testing.T) {
	m := Message{
		From:    "Radar <radar@example.com>",
		To:      []string{"me@example.org"},
		Subject: "Radar · Mon 9 Nov · 3 papers 👍",
		Text:    "plain body " + strings.Repeat("x", 100),
		HTML:    `<p style="color:#333">héllo</p>`,
		Date:    time.Date(2026, 11, 9, 7, 0, 0, 0, time.UTC),
	}
	raw, err := m.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	msg, err := netmail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	subj, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || subj != m.Subject {
		t.Errorf("subject = %q (%v)", subj, err)
	}
	if !strings.HasSuffix(msg.Header.Get("Message-ID"), "@example.com>") {
		t.Errorf("message-id = %q", msg.Header.Get("Message-ID"))
	}
	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/alternative" {
		t.Fatalf("content-type %q: %v", mt, err)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	var parts []string
	for {
		p, err := mr.NextPart() // decodes quoted-printable transparently
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p)
		parts = append(parts, p.Header.Get("Content-Type")+"|"+string(b))
	}
	if len(parts) != 2 || parts[0] != "text/plain; charset=utf-8|"+m.Text || parts[1] != "text/html; charset=utf-8|"+m.HTML {
		t.Errorf("parts = %q", parts)
	}
	for line := range strings.SplitSeq(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Errorf("line exceeds RFC 5322 limit: %d", len(line))
		}
	}
}

// fakeSMTP is a minimal SMTP server: EHLO, optional STARTTLS, AUTH PLAIN,
// MAIL, RCPT, DATA, QUIT. It records what it received.
type fakeSMTP struct {
	ln       net.Listener
	tlsCfg   *tls.Config
	startTLS bool
	user     string
	pass     string

	got chan received
}

type received struct {
	from, auth string
	to         []string
	data       string
	tls        bool
}

func newFakeSMTP(t *testing.T, implicit, startTLS bool) *fakeSMTP {
	t.Helper()
	cert := selfSigned(t)
	f := &fakeSMTP{tlsCfg: &tls.Config{Certificates: []tls.Certificate{cert}}, startTLS: startTLS,
		user: "u", pass: "p", got: make(chan received, 1)}
	var err error
	if implicit {
		f.ln, err = tls.Listen("tcp", "127.0.0.1:0", f.tlsCfg)
	} else {
		f.ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.ln.Close() })
	go f.serve(implicit)
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve(implicit bool) {
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	r, w := bufio.NewReader(conn), conn
	rec := received{tls: implicit}
	reply := func(s string) { io.WriteString(w, s+"\r\n") }
	reply("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch cmd {
		case "EHLO", "HELO":
			exts := []string{"250-fake"}
			if f.startTLS && !rec.tls {
				exts = append(exts, "250-STARTTLS")
			}
			if rec.tls {
				exts = append(exts, "250-AUTH PLAIN")
			}
			exts = append(exts, "250 8BITMIME")
			reply(strings.Join(exts, "\r\n"))
		case "STARTTLS":
			reply("220 go ahead")
			tc := tls.Server(conn, f.tlsCfg)
			if tc.Handshake() != nil {
				return
			}
			conn, r, w, rec.tls = tc, bufio.NewReader(tc), tc, true
		case "AUTH":
			b, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
			rec.auth = string(b)
			if rec.auth != "\x00"+f.user+"\x00"+f.pass {
				reply("535 bad credentials")
				continue
			}
			reply("235 ok")
		case "MAIL":
			rec.from = line
			reply("250 ok")
		case "RCPT":
			rec.to = append(rec.to, line)
			reply("250 ok")
		case "DATA":
			reply("354 send it")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			rec.data = b.String()
			reply("250 queued")
		case "QUIT":
			reply("221 bye")
			f.got <- rec
			return
		default:
			reply("502 unknown")
		}
	}
}

func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func testMessage() Message {
	return Message{From: "Radar <radar@example.com>", To: []string{"Me <me@example.org>"}, Subject: "hi",
		Text: "line one\n.leading dot\n", HTML: "<p>hi</p>"}
}

func TestSendModes(t *testing.T) {
	cases := []struct {
		name               string
		mode               TLSMode
		implicit, startTLS bool
		auth               bool
	}{
		{"starttls+auth", StartTLS, false, true, true},
		{"implicit+auth", Implicit, true, false, true},
		{"none", NoTLS, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newFakeSMTP(t, c.implicit, c.startTLS)
			s := &SMTP{Host: "127.0.0.1", Port: srv.port(), TLS: c.mode, Timeout: 5 * time.Second,
				TLSConfig: &tls.Config{InsecureSkipVerify: true}}
			if c.auth {
				s.Username, s.Password = "u", "p"
			}
			if err := s.Send(context.Background(), testMessage()); err != nil {
				t.Fatal(err)
			}
			rec := <-srv.got
			if !strings.HasPrefix(rec.from, "MAIL FROM:<radar@example.com>") {
				t.Errorf("from = %q", rec.from)
			}
			if len(rec.to) != 1 || !strings.HasPrefix(rec.to[0], "RCPT TO:<me@example.org>") {
				t.Errorf("to = %q", rec.to)
			}
			if c.auth && rec.auth != "\x00u\x00p" {
				t.Errorf("auth = %q", rec.auth)
			}
			if rec.tls != (c.mode != NoTLS) {
				t.Errorf("tls = %v", rec.tls)
			}
			if !strings.Contains(rec.data, "Subject: hi") || !strings.Contains(rec.data, ".leading dot") {
				t.Errorf("data = %q", rec.data)
			}
		})
	}
}

func TestStartTLSRequired(t *testing.T) {
	srv := newFakeSMTP(t, false, false) // server without STARTTLS
	s := &SMTP{Host: "127.0.0.1", Port: srv.port(), TLS: StartTLS, Timeout: 5 * time.Second}
	err := s.Send(context.Background(), testMessage())
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("err = %v, want STARTTLS refusal", err)
	}
}

func TestSendBadAddress(t *testing.T) {
	m := testMessage()
	m.To = []string{"not an address"}
	if err := (&SMTP{Host: "127.0.0.1", Port: 1}).Send(context.Background(), m); err == nil {
		t.Fatal("expected address error")
	}
}
