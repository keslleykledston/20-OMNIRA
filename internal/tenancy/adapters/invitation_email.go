package adapters

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	texttemplate "text/template"
	"time"

	"github.com/google/uuid"
)

// InvitationMessage é tudo que o remetente precisa para entregar um convite.
// AcceptURL é absoluta (host do frontend) e contém o token bruto: nunca deve ser
// logada nem persistida por quem implementa InvitationSender.
type InvitationMessage struct {
	To           string
	TenantName   string
	InviterEmail string
	AcceptURL    string
	ExpiresAt    time.Time
}

// SMTP TLS modes.
const (
	SMTPTLSStartTLS = "starttls" // padrão: exige STARTTLS
	SMTPTLSImplicit = "implicit" // TLS direto (porta 465)
	SMTPTLSNone     = "none"     // sem TLS: só dev/teste (Mailpit)
)

type SMTPConfig struct {
	Host, Username, Password string
	Port                     int
	From, ReplyTo            string
	TLSMode                  string
	Timeout                  time.Duration
}

// SMTPInvitationSender entrega convites por SMTP usando só a stdlib.
type SMTPInvitationSender struct {
	cfg     SMTPConfig
	from    *mail.Address
	replyTo *mail.Address
}

func NewSMTPInvitationSender(cfg SMTPConfig) (*SMTPInvitationSender, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, errors.New("smtp: host is required")
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	if cfg.TLSMode == "" {
		cfg.TLSMode = SMTPTLSStartTLS
	}
	switch cfg.TLSMode {
	case SMTPTLSStartTLS, SMTPTLSImplicit, SMTPTLSNone:
	default:
		return nil, fmt.Errorf("smtp: invalid TLS mode %q (starttls|implicit|none)", cfg.TLSMode)
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	from, err := parseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("smtp: invalid from address: %w", err)
	}
	s := &SMTPInvitationSender{cfg: cfg, from: from}
	if strings.TrimSpace(cfg.ReplyTo) != "" {
		if s.replyTo, err = parseAddress(cfg.ReplyTo); err != nil {
			return nil, fmt.Errorf("smtp: invalid reply-to address: %w", err)
		}
	}
	return s, nil
}

func parseAddress(v string) (*mail.Address, error) {
	if strings.ContainsAny(v, "\r\n") {
		return nil, errors.New("line breaks are not allowed")
	}
	return mail.ParseAddress(strings.TrimSpace(v))
}

func (s *SMTPInvitationSender) Send(ctx context.Context, msg InvitationMessage) error {
	to, err := parseAddress(msg.To)
	if err != nil {
		return fmt.Errorf("smtp: invalid recipient: %w", err)
	}
	body, err := renderInvitationEmail(s.from, s.replyTo, to, msg, time.Now().UTC())
	if err != nil {
		return err
	}
	return s.deliver(ctx, to.Address, body)
}

func (s *SMTPInvitationSender) deliver(ctx context.Context, rcpt string, body []byte) error {
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	dialer := &net.Dialer{Timeout: s.cfg.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp: connect: %w", err)
	}
	deadline := time.Now().Add(s.cfg.Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	tlsCfg := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}
	encrypted := false
	if s.cfg.TLSMode == SMTPTLSImplicit {
		conn = tls.Client(conn, tlsCfg)
		encrypted = true
	}
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp: handshake: %w", err)
	}
	defer c.Close()
	if s.cfg.TLSMode == SMTPTLSStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("smtp: server does not support STARTTLS")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("smtp: starttls: %w", err)
		}
		encrypted = true
	}
	if s.cfg.Username != "" {
		if !encrypted {
			return errors.New("smtp: refusing to send credentials without TLS")
		}
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return fmt.Errorf("smtp: auth: %w", err)
		}
	}
	if err := c.Mail(s.from.Address); err != nil {
		return fmt.Errorf("smtp: mail from: %w", err)
	}
	if err := c.Rcpt(rcpt); err != nil {
		return fmt.Errorf("smtp: rcpt to: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp: data: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("smtp: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: finish: %w", err)
	}
	return c.Quit()
}

var invitationTextTemplate = texttemplate.Must(texttemplate.New("text").Parse(`Olá,

{{.Inviter}} convidou você para acessar {{.Tenant}} no OMNIRA.

Para aceitar o convite, abra o link abaixo e entre com o mesmo e-mail em que recebeu esta mensagem:

{{.URL}}

O link é pessoal, pode ser usado uma única vez e expira em {{.Expires}}.
Se você não esperava este convite, ignore esta mensagem.
`))

var invitationHTMLTemplate = htmltemplate.Must(htmltemplate.New("html").Parse(`<!doctype html>
<html lang="pt-BR"><body style="font-family:-apple-system,Segoe UI,Helvetica,Arial,sans-serif;color:#1c1c1e;line-height:1.5">
<p>Olá,</p>
<p><strong>{{.Inviter}}</strong> convidou você para acessar <strong>{{.Tenant}}</strong> no OMNIRA.</p>
<p>Para aceitar o convite, use o botão abaixo e entre com o mesmo e-mail em que recebeu esta mensagem:</p>
<p><a href="{{.URL}}" style="display:inline-block;padding:10px 18px;background:#0a84ff;color:#fff;border-radius:8px;text-decoration:none">Aceitar convite</a></p>
<p style="font-size:13px;color:#636366">Se o botão não funcionar, copie este endereço: {{.URL}}</p>
<p style="font-size:13px;color:#636366">O link é pessoal, pode ser usado uma única vez e expira em {{.Expires}}.<br>Se você não esperava este convite, ignore esta mensagem.</p>
</body></html>
`))

// oneLine remove quebras de linha de valores vindos de dados (nome do tenant, e-mail do
// convidante) antes de entrarem em cabeçalhos ou no corpo.
func oneLine(s string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(s))
}

func renderInvitationEmail(from, replyTo, to *mail.Address, msg InvitationMessage, now time.Time) ([]byte, error) {
	inviter := oneLine(msg.InviterEmail)
	if inviter == "" {
		inviter = "Um administrador"
	}
	tenant := oneLine(msg.TenantName)
	if tenant == "" {
		tenant = "uma organização"
	}
	data := map[string]string{
		"Inviter": inviter, "Tenant": tenant, "URL": msg.AcceptURL,
		"Expires": msg.ExpiresAt.UTC().Format("02/01/2006 15:04 (UTC)"),
	}
	var text, html bytes.Buffer
	if err := invitationTextTemplate.Execute(&text, data); err != nil {
		return nil, err
	}
	if err := invitationHTMLTemplate.Execute(&html, data); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	hdr := func(k, v string) string { return k + ": " + v + "\r\n" }
	var head strings.Builder
	head.WriteString(hdr("From", from.String()))
	head.WriteString(hdr("To", to.String()))
	if replyTo != nil {
		head.WriteString(hdr("Reply-To", replyTo.String()))
	}
	head.WriteString(hdr("Subject", mime.QEncoding.Encode("utf-8", "Convite para "+tenant+" no OMNIRA")))
	head.WriteString(hdr("Date", now.Format(time.RFC1123Z)))
	domain := "omnira.invalid"
	if at := strings.LastIndexByte(from.Address, '@'); at >= 0 {
		domain = from.Address[at+1:]
	}
	head.WriteString(hdr("Message-ID", "<"+uuid.NewString()+"@"+domain+">"))
	head.WriteString(hdr("MIME-Version", "1.0"))
	head.WriteString(hdr("Content-Type", "multipart/alternative; boundary="+mw.Boundary()))

	for _, p := range []struct {
		ctype string
		body  []byte
	}{{"text/plain", text.Bytes()}, {"text/html", html.Bytes()}} {
		part, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {p.ctype + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(part)
		if _, err := io.Copy(qp, bytes.NewReader(p.body)); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return append([]byte(head.String()+"\r\n"), buf.Bytes()...), nil
}
