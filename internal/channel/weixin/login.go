package weixin

import (
	"context"
	"errors"
	"net/url"
	"os"
	"time"
)

type qrResponse struct {
	Code    string `json:"qrcode"`
	Content string `json:"qrcode_img_content"`
}

type loginResponse struct {
	Status       string `json:"status"`
	Token        string `json:"bot_token"`
	AccountID    string `json:"ilink_bot_id"`
	UserID       string `json:"ilink_user_id"`
	BaseURL      string `json:"baseurl"`
	RedirectHost string `json:"redirect_host"`
}

// LoginUI is supplied only by the interactive CLI, never the logging system.
type LoginUI struct {
	ShowQR           func(string) error
	VerificationCode func(context.Context) (string, error)
}

// Login supports the reference client's QR states, a five-minute deadline and
// at most three refreshes. Saved credentials do not grant Folio authorization.
func Login(ctx context.Context, disk *Disk, ui LoginUI) (Credentials, error) {
	return login(ctx, disk, newClient(DefaultBaseURL, ""), ui)
}

func login(ctx context.Context, disk *Disk, c *Client, ui LoginUI) (Credentials, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if ui.ShowQR == nil {
		return Credentials{}, ErrProtocol
	}
	var old Credentials
	err := disk.read("credentials.json", &old)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Credentials{}, err
	}
	tokens := []string{}
	if old.Token != "" {
		tokens = append(tokens, old.Token)
	}
	for refresh := 0; refresh < 4; refresh++ {
		// Each fresh QR starts on the fixed official base. Test clients retain
		// their local transport; server redirects are still explicitly checked.
		base := c.baseURL
		var qr qrResponse
		if err := c.request(ctx, "get_bot_qrcode?bot_type=3", map[string]any{"local_token_list": tokens}, false, 15*time.Second, &qr); err != nil {
			return Credentials{}, err
		}
		if !validOpaque(qr.Code, 8192) || !validOpaque(qr.Content, 8192) {
			return Credentials{}, ErrProtocol
		}
		if err := ui.ShowQR(qr.Content); err != nil {
			return Credentials{}, ErrUnavailable
		}
		code := ""
		refreshQR := false
		for !refreshQR {
			if ctx.Err() != nil {
				return Credentials{}, ctx.Err()
			}
			query := url.Values{"qrcode": {qr.Code}}
			if code != "" {
				query.Set("verify_code", code)
			}
			var status loginResponse
			err := c.request(ctx, "get_qrcode_status?"+query.Encode(), nil, false, 35*time.Second, &status)
			if err != nil {
				if errors.Is(err, ErrSessionExpired) || errors.Is(err, ErrProtocol) {
					return Credentials{}, err
				}
				if err := delay(ctx, time.Second); err != nil {
					return Credentials{}, err
				}
				continue
			}
			switch status.Status {
			case "confirmed":
				if status.BaseURL == "" {
					status.BaseURL = c.baseURL
				}
				creds := Credentials{Token: status.Token, AccountID: status.AccountID, UserID: status.UserID, BaseURL: status.BaseURL}
				if err := disk.saveCredentials(creds); err != nil {
					return Credentials{}, err
				}
				return creds, nil
			case "scaned_but_redirect":
				next := "https://" + status.RedirectHost
				if !validBaseURL(next) {
					return Credentials{}, ErrProtocol
				}
				c.baseURL = next
			case "need_verifycode":
				if ui.VerificationCode == nil {
					return Credentials{}, errors.New("weixin: interactive verification required")
				}
				code, err = ui.VerificationCode(ctx)
				if err != nil {
					return Credentials{}, err
				}
				if len(code) < 1 || len(code) > 12 {
					return Credentials{}, ErrProtocol
				}
				for _, ch := range code {
					if ch < '0' || ch > '9' {
						return Credentials{}, ErrProtocol
					}
				}
				continue
			case "scaned":
				code = ""
			case "expired", "verify_code_blocked":
				refreshQR = true
			case "binded_redirect":
				return disk.Credentials() // Only succeed if local credentials exist.
			case "wait":
			default:
				return Credentials{}, ErrProtocol
			}
			if err := delay(ctx, time.Second); err != nil {
				return Credentials{}, err
			}
		}
		c.baseURL = base
	}
	return Credentials{}, errors.New("weixin: QR login expired; try again")
}

func delay(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
