package adapters

import (
	"bufio"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeSMTP is a minimal plaintext SMTP server (no STARTTLS) that records one message.
type fakeSMTP struct {
	ln       net.Listener
	from, to string
	data     string
	done     chan struct{}
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, done: make(chan struct{})}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		defer close(f.done)
		rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
		reply := func(s string) { _, _ = rw.WriteString(s + "\r\n"); _ = rw.Flush() }
		reply("220 fake ESMTP")
		for {
			line, err := rw.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				reply("250 fake")
			case strings.HasPrefix(cmd, "MAIL FROM:"):
				f.from = strings.Trim(strings.TrimSpace(line[len("MAIL FROM:"):]), "<>")
				reply("250 ok")
			case strings.HasPrefix(cmd, "RCPT TO:"):
				f.to = strings.Trim(strings.TrimSpace(line[len("RCPT TO:"):]), "<>")
				reply("250 ok")
			case cmd == "DATA":
				reply("354 go")
				var sb strings.Builder
				for {
					l, err := rw.ReadString('\n')
					if err != nil || l == ".\r\n" {
						break
					}
					sb.WriteString(l)
				}
				f.data = sb.String()
				reply("250 queued")
			case cmd == "QUIT":
				reply("221 bye")
				return
			default:
				reply("250 ok")
			}
		}
	}()
	return f
}

func (f *fakeSMTP) hostPort(t *testing.T) (string, int) {
	t.Helper()
	host, p, _ := net.SplitHostPort(f.ln.Addr().String())
	port, _ := strconv.Atoi(p)
	return host, port
}

func testMessage() InvitationMessage {
	return InvitationMessage{
		To: "convidado@empresa.com", TenantName: "Acme Telecom", InviterEmail: "admin@acme.com",
		AcceptURL: "https://app.test/invite/TOKEN_abc-123", ExpiresAt: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC),
	}
}

func TestSMTPSenderDeliversAWellFormedMultipartMessage(t *testing.T) {
	srv := newFakeSMTP(t)
	host, port := srv.hostPort(t)
	s, err := NewSMTPInvitationSender(SMTPConfig{
		Host: host, Port: port, TLSMode: SMTPTLSNone,
		From: "OMNIRA <no-reply@omnira.test>", ReplyTo: "suporte@omnira.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), testMessage()); err != nil {
		t.Fatalf("send: %v", err)
	}
	<-srv.done
	if srv.from != "no-reply@omnira.test" || srv.to != "convidado@empresa.com" {
		t.Fatalf("envelope from=%q to=%q", srv.from, srv.to)
	}
	msg, err := mail.ReadMessage(strings.NewReader(srv.data))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := msg.Header.Get("Reply-To"); !strings.Contains(got, "suporte@omnira.test") {
		t.Errorf("Reply-To = %q", got)
	}
	subj, _ := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if subj != "Convite para Acme Telecom no OMNIRA" {
		t.Errorf("subject = %q", subj)
	}
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		t.Fatalf("content-type = %q %v", mediaType, err)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	var kinds []string
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(quotedprintable.NewReader(part))
		text := string(body)
		kinds = append(kinds, part.Header.Get("Content-Type"))
		if !strings.Contains(text, "https://app.test/invite/TOKEN_abc-123") || !strings.Contains(text, "Acme Telecom") ||
			!strings.Contains(text, "admin@acme.com") || !strings.Contains(text, "25/09/2026 12:00 (UTC)") {
			t.Errorf("part %s is missing invitation data:\n%s", part.Header.Get("Content-Type"), text)
		}
	}
	if len(kinds) != 2 || !strings.HasPrefix(kinds[0], "text/plain") || !strings.HasPrefix(kinds[1], "text/html") {
		t.Fatalf("expected text/plain + text/html, got %v", kinds)
	}
}

func TestInvitationEmailCannotBeHeaderInjected(t *testing.T) {
	from, _ := mail.ParseAddress("no-reply@omnira.test")
	to, _ := mail.ParseAddress("convidado@empresa.com")
	m := testMessage()
	m.TenantName = "Acme\r\nBcc: atacante@evil.test"
	m.InviterEmail = "admin@acme.com\r\nX-Injected: 1"
	raw, err := renderInvitationEmail(from, nil, to, m, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"Bcc", "X-Injected"} {
		if parsed.Header.Get(h) != "" {
			t.Fatalf("header %s was injected", h)
		}
	}
}

func TestSMTPSenderValidatesConfigAndRefusesUnsafeAuth(t *testing.T) {
	if _, err := NewSMTPInvitationSender(SMTPConfig{Host: "", From: "a@b.co"}); err == nil {
		t.Error("empty host must be rejected")
	}
	if _, err := NewSMTPInvitationSender(SMTPConfig{Host: "h", From: "not an address"}); err == nil {
		t.Error("invalid from must be rejected")
	}
	if _, err := NewSMTPInvitationSender(SMTPConfig{Host: "h", From: "a@b.co", TLSMode: "bogus"}); err == nil {
		t.Error("invalid TLS mode must be rejected")
	}
	if _, err := NewSMTPInvitationSender(SMTPConfig{Host: "h", From: "a@b.co\r\nBcc: x@y.z"}); err == nil {
		t.Error("CRLF in from must be rejected")
	}

	// credentials must never travel without TLS
	srv := newFakeSMTP(t)
	host, port := srv.hostPort(t)
	s, err := NewSMTPInvitationSender(SMTPConfig{Host: host, Port: port, TLSMode: SMTPTLSNone, From: "a@b.co", Username: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), testMessage()); err == nil || !strings.Contains(err.Error(), "without TLS") {
		t.Fatalf("expected refusal to send credentials without TLS, got %v", err)
	}

	// STARTTLS is required by default: a server that does not offer it is a failure, not a downgrade
	srv2 := newFakeSMTP(t)
	h2, p2 := srv2.hostPort(t)
	s2, _ := NewSMTPInvitationSender(SMTPConfig{Host: h2, Port: p2, From: "a@b.co"})
	if err := s2.Send(context.Background(), testMessage()); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("expected STARTTLS failure, got %v", err)
	}
}

func TestSMTPSendErrorNeverContainsTheLink(t *testing.T) {
	s, _ := NewSMTPInvitationSender(SMTPConfig{Host: "127.0.0.1", Port: 1, TLSMode: SMTPTLSNone, From: "a@b.co", Timeout: time.Second})
	err := s.Send(context.Background(), testMessage())
	if err == nil {
		t.Fatal("connecting to a closed port must fail")
	}
	if strings.Contains(err.Error(), "TOKEN_abc") {
		t.Fatal("errors must not carry the invitation link/token")
	}
}
