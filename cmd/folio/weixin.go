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
		creds, err := disk.Credentials()
		if err != nil {
			return err
		}
		// Local configuration presence is not a live token-validity check.
		return json.NewEncoder(out).Encode(map[string]any{"configured": true, "scanner_id": creds.UserID})
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
	_, err = fmt.Fprintf(out, "已保存登录凭据。扫码用户 ID：%s\n核对身份后配置 WEIXIN_OWNER_ID，再启用 WEIXIN_ENABLED。\n", creds.UserID)
	return err
}

// prepareWeixin fails closed before starting HTTP when enabled configuration
// or private state is invalid. The returned function owns the channel lock.
func prepareWeixin(layout string, backend *store.Store) (func(context.Context), error) {
	if !envBool("WEIXIN_ENABLED") {
		return nil, nil
	}
	disk, err := weixin.OpenDisk(filepath.Join(layout, "secrets", "weixin"))
	if err != nil {
		return nil, err
	}
	creds, err := disk.Credentials()
	if err != nil {
		_ = disk.Close()
		return nil, err
	}
	handler, err := backend.WeixinHandler(creds.AccountID, os.Getenv("WEIXIN_OWNER_ID"))
	if err != nil {
		_ = disk.Close()
		return nil, err
	}
	service, err := weixin.NewService(disk, handler)
	if err != nil {
		_ = disk.Close()
		return nil, err
	}
	return func(ctx context.Context) {
		defer disk.Close()
		if err := service.Run(ctx); err != nil && ctx.Err() == nil {
			// Fixed classes only. Never log peer IDs, text, cursors or tokens.
			if errors.Is(err, weixin.ErrSessionExpired) {
				fmt.Fprintln(os.Stderr, "folio: weixin login expired; stop service and run weixin login")
			} else {
				fmt.Fprintln(os.Stderr, "folio: weixin channel stopped; check private configuration and state")
			}
		}
	}, nil
}
