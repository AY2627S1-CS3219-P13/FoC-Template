package user

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"time"
)

type SMTPMailer struct{ Config Config }

func (m SMTPMailer) SendVerification(ctx context.Context, email, code string) error {
	cfg := m.Config
	host, _, err := net.SplitHostPort(cfg.SMTPAddress)
	if err != nil {
		return err
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", cfg.SMTPAddress)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if cfg.SMTPTLS {
		if err = client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if cfg.SMTPUsername != "" {
		if err = client.Auth(smtp.PlainAuth("", cfg.SMTPUsername, cfg.SMTPPassword, host)); err != nil {
			return err
		}
	}
	if err = client.Mail(cfg.SMTPFrom); err != nil {
		return err
	}
	if err = client.Rcpt(email); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "From: %s\r\nTo: %s\r\nSubject: Verify your Friend on Campus account\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nYour verification code is: %s\r\n\r\nIt expires in 30 minutes. If you did not register, ignore this email.\r\n", cfg.SMTPFrom, email, code)
	if err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	return client.Quit()
}
