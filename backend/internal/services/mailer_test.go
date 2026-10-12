package services

import "testing"

func TestMailer(t *testing.T) {
	if NewMailer("", 587, "", "", "x@y.z") != nil {
		t.Fatal("empty host must disable mail")
	}
	var off *Mailer
	off.SendAsync("a@b.c", "s", "b") // nil mailer is a no-op, must not panic
	if got := envelopeAddr("SparkLabX <support@sparklabx.com>"); got != "support@sparklabx.com" {
		t.Fatalf("envelopeAddr = %q", got)
	}
	if got := envelopeAddr("support@sparklabx.com"); got != "support@sparklabx.com" {
		t.Fatalf("envelopeAddr = %q", got)
	}
	m := NewMailer("127.0.0.1", 1, "", "", "a@b.c")
	if err := m.send("victim@x.com\r\nBcc: all@x.com", "s", "b"); err == nil || err.Error() != "invalid header value" {
		t.Fatalf("header injection not rejected: %v", err)
	}
}
