package mailer

import (
	"context"
	"net"
	"testing"
)

func TestNewSMTPValidatesConfiguration(t *testing.T) {
	if _, err := NewSMTP("smtp.example.test", 587, "user", "", "Shelve <noreply@example.test>"); err == nil {
		t.Fatal("expected incomplete SMTP credentials to be rejected")
	}
	if _, err := NewSMTP("smtp.example.test", 70000, "", "", "noreply@example.test"); err == nil {
		t.Fatal("expected invalid SMTP port to be rejected")
	}
	if _, err := NewSMTP("smtp.example.test", 587, "", "", "Shelve <noreply@example.test>"); err != nil {
		t.Fatalf("valid SMTP configuration rejected: %v", err)
	}
}

func TestSendRejectsSMTPWithoutStartTLS(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		_, _ = connection.Write([]byte("220 local test server\r\n"))
		buffer := make([]byte, 1024)
		_, _ = connection.Read(buffer)
		_, _ = connection.Write([]byte("250-local test server\r\n250 HELP\r\n"))
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	sender, err := NewSMTP("127.0.0.1", port, "", "", "noreply@example.test")
	if err != nil {
		t.Fatal(err)
	}
	err = sender.Send(context.Background(), "reader@example.test", "Test", "Body")
	if err == nil {
		t.Fatalf("expected missing STARTTLS to fail, got %v", err)
	}
	if err.Error() != "SMTP server does not support required STARTTLS encryption" {
		t.Fatalf("unexpected SMTP failure: %v", err)
	}
}
