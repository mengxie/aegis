package pii

import (
	"strings"
	"testing"
)

func TestScanFindsSensitiveData(t *testing.T) {
	cases := []struct {
		name, text string
		kind       Kind
		value      string
	}{
		{"email", "Contact aroha.ngata@acme.co.nz today", Email, "aroha.ngata@acme.co.nz"},
		{"international phone", "Call +64 21 555 0199 after 5", Phone, "+64 21 555 0199"},
		{"local phone", "or (09) 555-0123", Phone, "(09) 555-0123"},
		{"visa card", "Card 4111 1111 1111 1111 expires", Card, "4111 1111 1111 1111"},
		{"ip address", "Login from 203.0.113.42 failed", IP, "203.0.113.42"},
		{"iban", "Pay GB82 WEST 1234 5698 7654 32 now", IBAN, "GB82 WEST 1234 5698 7654 32"},
		{"aws key", "key AKIAIOSFODNN7EXAMPLE in env", Secret, "AKIAIOSFODNN7EXAMPLE"},
		{"github token", "ghp_" + strings.Repeat("a1B2", 9), Secret, "ghp_" + strings.Repeat("a1B2", 9)},
		{"private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIabc\n-----END RSA PRIVATE KEY-----", Secret, "-----BEGIN RSA PRIVATE KEY-----\nMIIabc\n-----END RSA PRIVATE KEY-----"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			findings := Scan(c.text)
			if len(findings) != 1 || findings[0].Kind != c.kind || findings[0].Value != c.value {
				t.Fatalf("Scan(%q) = %+v, want one %s %q", c.text, findings, c.kind, c.value)
			}
		})
	}
}

func TestScanIgnoresLookalikes(t *testing.T) {
	for _, text := range []string{
		"Card 4111 1111 1111 1112 fails the Luhn check",
		"Version 999.1.1.1 is not an IP address",
		"Order 123456789 and invoice 20261030 are not phone numbers",
		"GB00 WEST 1234 5698 7654 32 fails mod-97",
		"Meet on 2026-10-30 at 10:30",
	} {
		if findings := Scan(text); len(findings) != 0 {
			t.Errorf("Scan(%q) = %+v, want nothing", text, findings)
		}
	}
}

func TestOverlappingMatchesPreferTheMostSpecific(t *testing.T) {
	findings := Scan("token sk-proj-abcdefghijklmnopqrstuvwxyz123456 and mail a@b.io")
	if len(findings) != 2 || findings[0].Kind != Secret || findings[1].Kind != Email {
		t.Fatalf("got %+v", findings)
	}
}

func TestPseudonymizeIsConsistentAndReversible(t *testing.T) {
	vault := NewVault()
	text := "From aroha@acme.co.nz to sam@acme.co.nz, cc aroha@acme.co.nz"
	masked, findings := vault.Pseudonymize(text)
	if want := "From [EMAIL_1] to [EMAIL_2], cc [EMAIL_1]"; masked != want {
		t.Fatalf("masked = %q, want %q", masked, want)
	}
	if len(findings) != 3 || vault.Size() != 2 {
		t.Fatalf("findings %d, vault %d", len(findings), vault.Size())
	}
	if restored := vault.Restore("Reply to [EMAIL_2] and [EMAIL_9]"); restored != "Reply to sam@acme.co.nz and [EMAIL_9]" {
		t.Fatalf("restored = %q (unknown placeholders must be left alone)", restored)
	}
}

func TestRedactIsIrreversible(t *testing.T) {
	redacted, _ := Redact("Email aroha@acme.co.nz from 203.0.113.42")
	if redacted != "Email [EMAIL] from [IP]" {
		t.Fatalf("redacted = %q", redacted)
	}
}

func TestStreamRestorerHoldsBackSplitPlaceholders(t *testing.T) {
	vault := NewVault()
	vault.Pseudonymize("aroha@acme.co.nz")
	r := vault.NewStreamRestorer()
	var out strings.Builder
	for _, chunk := range []string{"Write to [EM", "AIL", "_1] about [", "draft] items [E"} {
		out.WriteString(r.Push(chunk))
	}
	out.WriteString(r.Flush())
	if want := "Write to aroha@acme.co.nz about [draft] items [E"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

// FuzzStreamRestorer proves that streaming never changes the result: however the text is cut into
// chunks, the restored stream equals restoring the whole text at once.
func FuzzStreamRestorer(f *testing.F) {
	f.Add("Mail [EMAIL_1] or [PHONE_1], not [EMAIL_2] [", uint8(3))
	f.Add("[[[EMAIL_1]]] [EMAIL_", uint8(1))
	f.Add("plain text", uint8(7))
	f.Fuzz(func(t *testing.T, text string, size uint8) {
		vault := NewVault()
		vault.Pseudonymize("aroha@acme.co.nz +64 21 555 0199")
		step := int(size%16) + 1
		r := vault.NewStreamRestorer()
		var out strings.Builder
		for i := 0; i < len(text); i += step {
			out.WriteString(r.Push(text[i:min(i+step, len(text))]))
		}
		out.WriteString(r.Flush())
		if want := vault.Restore(text); out.String() != want {
			t.Fatalf("streamed %q, whole %q", out.String(), want)
		}
	})
}
