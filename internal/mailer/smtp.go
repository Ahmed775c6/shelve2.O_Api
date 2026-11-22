package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

type Sender interface {
	Send(context.Context, string, string, string) error
}

type SMTP struct {
	address  string
	host     string
	username string
	password string
	from     string
	envelope string
}

func NewSMTP(host string, port int, username, password, from string) (*SMTP, error) {
	host = strings.TrimSpace(host)
	fromAddress, err := mail.ParseAddress(strings.TrimSpace(from))
	if err != nil || host == "" || strings.ContainsAny(host, "\r\n") {
		return nil, errors.New("SMTP host and sender address must be valid")
	}
	if port < 1 || port > 65535 {
		return nil, errors.New("SMTP port must be between 1 and 65535")
	}
	if (username == "") != (password == "") {
		return nil, errors.New("SMTP username and password must both be set or both be empty")
	}
	return &SMTP{
		address:  net.JoinHostPort(host, strconv.Itoa(port)),
		host:     host,
		username: username,
		password: password,
		from:     fromAddress.String(),
		envelope: fromAddress.Address,
	}, nil
}

func (s *SMTP) Send(ctx context.Context, to, subject, body string) error {
	toAddress, err := mail.ParseAddress(strings.TrimSpace(to))
	if err != nil || strings.ContainsAny(subject, "\r\n") {
		return errors.New("recipient or subject is invalid")
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", s.address)
	if err != nil {
		return fmt.Errorf("connect to SMTP server: %w", err)
	}
	defer connection.Close()
	deadline := time.Now().Add(20 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set SMTP deadline: %w", err)
	}

	client, err := smtp.NewClient(connection, s.host)
	if err != nil {
		return fmt.Errorf("initialize SMTP client: %w", err)
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); !ok {
		return errors.New("SMTP server does not support required STARTTLS encryption")
	}
	if err := client.StartTLS(&tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12}); err != nil {
		return fmt.Errorf("start encrypted SMTP connection: %w", err)
	}
	if s.username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.username, s.password, s.host)); err != nil {
			return fmt.Errorf("authenticate with SMTP server: %w", err)
		}
	}
	if err := client.Mail(s.envelope); err != nil {
		return fmt.Errorf("set SMTP sender: %w", err)
	}
	if err := client.Rcpt(toAddress.Address); err != nil {
		return fmt.Errorf("set SMTP recipient: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("begin SMTP message: %w", err)
	}
	message := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n",
		s.from, toAddress.String(), subject, strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"),
	)
	if _, err := writer.Write([]byte(message)); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write SMTP message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish SMTP message: %w", err)
	}
	return nil
}
