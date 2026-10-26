package main

import (
	"io"
	"strings"
	"testing"

	"folio/internal/store"
)

func TestWeixinDisabledDoesNotTouchStateOrNetwork(t *testing.T) {
	t.Setenv("WEIXIN_ENABLED", "false")
	if start, err := prepareWeixin("/not-a-runtime", nil); err != nil || start != nil {
		t.Fatal("disabled channel was initialized")
	}
}

func TestWeixinEnabledRequiresCredentialsAndExplicitOwner(t *testing.T) {
	t.Setenv("WEIXIN_ENABLED", "true")
	t.Setenv("WEIXIN_OWNER_ID", "")
	if _, err := prepareWeixin(t.TempDir(), &store.Store{}); err == nil {
		t.Fatal("enabled without credentials")
	}
	if _, err := (&store.Store{}).WeixinHandler("synthetic-bot", ""); err == nil {
		t.Fatal("missing explicit owner accepted")
	}
}

func TestWeixinOperatorArgumentsAndSourceBoundary(t *testing.T) {
	for _, args := range [][]string{{}, {"unknown"}, {"status", "unexpected"}, {"login", "--data-dir", "../.."}} {
		if err := weixinCommand(args, strings.NewReader(""), io.Discard); err == nil {
			t.Fatal("invalid operator flow accepted")
		}
	}
}
