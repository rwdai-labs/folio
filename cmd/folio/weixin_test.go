package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"folio/internal/channel/weixin"
	"folio/internal/store"
)

// The deployment variable is tri-state on purpose: unset means the console owns
// the decision, while an explicit value must win over whatever the page saved.
func TestWeixinModeIsTriState(t *testing.T) {
	previous, had := os.LookupEnv("WEIXIN_ENABLED")
	defer func() {
		if had {
			_ = os.Setenv("WEIXIN_ENABLED", previous)
			return
		}
		_ = os.Unsetenv("WEIXIN_ENABLED")
	}()
	if err := os.Unsetenv("WEIXIN_ENABLED"); err != nil {
		t.Fatal(err)
	}
	if mode := weixinMode(); mode != weixin.ModeAuto {
		t.Fatalf("unset mode = %q", mode)
	}

	for _, probe := range []struct {
		value string
		want  weixin.Mode
	}{
		{"", weixin.ModeAuto},
		{"   ", weixin.ModeAuto},
		{"true", weixin.ModeOn},
		{"TRUE", weixin.ModeOn},
		{"1", weixin.ModeOn},
		{"on", weixin.ModeOn},
		{"false", weixin.ModeOff},
		{"0", weixin.ModeOff},
		{"no", weixin.ModeOff},
	} {
		t.Setenv("WEIXIN_ENABLED", probe.value)
		if mode := weixinMode(); mode != probe.want {
			t.Fatalf("%q mode = %q, want %q", probe.value, mode, probe.want)
		}
	}
}

func TestWeixinSupervisorHonoursDeploymentMode(t *testing.T) {
	// ModeOff touches neither the state directory nor the network.
	off := weixin.NewSupervisor("/not-a-runtime", weixin.ModeOff, "", nil)
	if err := off.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := off.Status(); status["state"] != "disabled" {
		t.Fatalf("kill switch status = %+v", status)
	}
	off.Close()

	// ModeOn is a pinned deployment: missing credentials must fail closed at
	// startup instead of silently idling.
	on := weixin.NewSupervisor(t.TempDir(), weixin.ModeOn, "owner", nil)
	if err := on.Start(context.Background()); err == nil {
		on.Close()
		t.Fatal("pinned mode started without credentials")
	}
	on.Close()
}

func TestWeixinEnabledRequiresExplicitOwner(t *testing.T) {
	if _, err := (&store.Store{}).WeixinHandler("synthetic-bot", ""); err == nil {
		t.Fatal("missing explicit owner accepted")
	}
	if _, err := (&store.Store{}).WeixinHandler("", "synthetic-owner"); err == nil {
		t.Fatal("missing account accepted")
	}
}

func TestWeixinOperatorArgumentsAndSourceBoundary(t *testing.T) {
	for _, args := range [][]string{{}, {"unknown"}, {"status", "unexpected"}, {"login", "--data-dir", "../.."}} {
		if err := weixinCommand(args, strings.NewReader(""), io.Discard); err == nil {
			t.Fatal("invalid operator flow accepted")
		}
	}
}
