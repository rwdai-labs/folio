package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestAnnouncePasswordReportsGeneratedCredential(t *testing.T) {
	var out bytes.Buffer
	announcePassword(&out, "0123456789abcdef", "/data/secrets/admin-password")
	notice := out.String()
	for _, want := range []string{
		"generated one for this first start",
		"administrator password: 0123456789abcdef",
		"/data/secrets/admin-password",
		"printed only on this first start",
		"keep container logs private",
	} {
		if !strings.Contains(notice, want) {
			t.Fatalf("notice does not mention %q: %q", want, notice)
		}
	}
}
