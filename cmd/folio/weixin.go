package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"folio/internal/channel/weixin"
	"folio/internal/store"

	qrcode "github.com/skip2/go-qrcode"
)

// weixinCommand is an operator-only local flow. Its QR and scanner identity
// output is deliberately never routed through service logs or HTTP handlers.
func weixinCommand(args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 || (args[0] != "login" && args[0] != "status") {
		return errors.New("weixin: use login or status")
	}
	flags := flag.NewFlagSet("weixin", flag.ContinueOnError)
	dataDir := flags.String("data-dir", env("SERVICE_DATA_DIR", "/data"), "private persistent runtime")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("invalid weixin arguments")
	}
	layout, err := store.ResolveLayout(*dataDir)
	if err != nil {
		return err
	}
	disk, err := weixin.OpenDisk(filepath.Join(layout, "secrets", "weixin"))
	if err != nil {
		return err
	}
	defer disk.Close()
	if args[0] == "status" {
		settings, err := disk.Settings()
		if err != nil {
			return err
		}
		value := map[string]any{"configured": false, "owner_confirmed": settings.OwnerID != "", "enabled": settings.Enabled}
		if disk.HasCredentials() {
			creds, err := disk.Credentials()
			if err != nil {
				return err
			}
			// Local configuration presence is not a live token-validity check.
			value["configured"], value["scanner_id"] = true, creds.UserID
		}
		return json.NewEncoder(out).Encode(value)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	lines := bufio.NewReaderSize(in, 128)
	creds, err := weixin.Login(ctx, disk, weixin.LoginUI{
		ShowQR: func(content string) error {
			qr, err := qrcode.New(content, qrcode.Medium)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(out, "请用微信扫码连接 Folio（二维码仅供本地终端查看）：\n"+qr.ToSmallString(false))
			return err
		},
		VerificationCode: func(ctx context.Context) (string, error) {
			fmt.Fprint(out, "输入手机微信显示的数字：")
			type result struct {
				text string
				err  error
			}
			done := make(chan result, 1)
			go func() {
				line, err := lines.ReadSlice('\n')
				done <- result{strings.TrimSpace(string(line)), err}
			}()
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case r := <-done:
				return r.text, r.err
			}
		},
	})
	if err != nil {
		return err
	}
	if err := disk.SaveSettings(weixin.Settings{Version: 1, PendingOwner: creds.UserID}); err != nil {
		return err
	}
	// A fresh scan always needs a fresh owner decision, in the console or by
	// pinning WEIXIN_OWNER_ID for a headless deployment.
	_, err = fmt.Fprintf(out, "已保存登录凭据。扫码用户 ID：%s\n请在 Folio 控制台核对身份并确认绑定；无控制台时设置 WEIXIN_OWNER_ID 与 WEIXIN_ENABLED=true。\n", creds.UserID)
	return err
}

// weixinMode reads the deployment switch. Unset leaves the decision to the
// authenticated console; an explicit true or false pins the channel so an
// operator can guarantee state regardless of what the page last saved.
func weixinMode() weixin.Mode {
	raw, ok := os.LookupEnv("WEIXIN_ENABLED")
	if !ok {
		return weixin.ModeAuto
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return weixin.ModeAuto
	case "1", "true", "yes", "on":
		return weixin.ModeOn
	default:
		return weixin.ModeOff
	}
}

// newWeixinSupervisor is the only owner of the private channel directory in the
// serving process. Login and polling share one lock handle, so the console can
// replace a session without a restart and without a second open of the lock.
func newWeixinSupervisor(layout string, backend *store.Store) *weixin.Supervisor {
	return weixin.NewSupervisor(
		filepath.Join(layout, "secrets", "weixin"),
		weixinMode(),
		strings.TrimSpace(os.Getenv("WEIXIN_OWNER_ID")),
		backend.WeixinHandler,
	)
}

// weixinConsole adapts the channel supervisor to the Web contract. The host
// keeps HTTP naming in internal/web; the transport package keeps its own.
type weixinConsole struct{ supervisor *weixin.Supervisor }

func (c weixinConsole) WeixinStatus() map[string]any { return c.supervisor.Status() }

func (c weixinConsole) WeixinStartLogin() (map[string]any, error) { return c.supervisor.StartLogin() }

func (c weixinConsole) WeixinLoginStatus() map[string]any { return c.supervisor.LoginStatus() }

func (c weixinConsole) WeixinSubmitCode(code string) error { return c.supervisor.SubmitCode(code) }

func (c weixinConsole) WeixinCancelLogin() { c.supervisor.CancelLogin() }

func (c weixinConsole) WeixinConfirm(ownerID string) (map[string]any, error) {
	return c.supervisor.Confirm(ownerID)
}

func (c weixinConsole) WeixinSetEnabled(enabled bool) (map[string]any, error) {
	return c.supervisor.SetEnabled(enabled)
}
