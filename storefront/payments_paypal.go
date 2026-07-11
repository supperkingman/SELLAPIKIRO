// payments_paypal.go - PayPal Orders API v2 (USD).
// Luong: client tao order qua JS SDK -> khach approve -> server capture + verify
// -> fulfill. Server tu lay access token (client credentials) va goi capture de
// khong tin tuong ket qua phia client.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var paypalClient = &http.Client{Timeout: 20 * time.Second}

// paypalBase tra ve endpoint theo che do live/sandbox.
func (app *App) paypalBase() string {
	if app.pay.PaypalLive {
		return "https://api-m.paypal.com"
	}
	return "https://api-m.sandbox.paypal.com"
}

// paypalToken lay access token qua client credentials.
func (app *App) paypalToken() (string, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	req, _ := http.NewRequest(http.MethodPost, app.paypalBase()+"/v1/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(app.pay.PaypalClientID, app.pay.PaypalSecret)
	resp, err := paypalClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("paypal token failed (%d)", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	return out.AccessToken, nil
}

// capturePaypalOrder capture 1 order (sau khi khach approve) va verify.
// Tra ve (soTienUSD, capltureID, loi).
func (app *App) capturePaypalOrder(paypalOrderID string) (float64, string, error) {
	tok, err := app.paypalToken()
	if err != nil {
		return 0, "", err
	}
	req, _ := http.NewRequest(http.MethodPost,
		app.paypalBase()+"/v2/checkout/orders/"+url.PathEscape(paypalOrderID)+"/capture",
		bytes.NewReader([]byte("{}")))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := paypalClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, "", fmt.Errorf("paypal capture failed (%d): %s", resp.StatusCode, string(data))
	}

	var out struct {
		Status        string `json:"status"`
		PurchaseUnits []struct {
			Payments struct {
				Captures []struct {
					ID     string `json:"id"`
					Amount struct {
						Value string `json:"value"`
					} `json:"amount"`
				} `json:"captures"`
			} `json:"payments"`
		} `json:"purchase_units"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return 0, "", err
	}
	if out.Status != "COMPLETED" {
		return 0, "", fmt.Errorf("paypal order chua COMPLETED (status=%s)", out.Status)
	}
	if len(out.PurchaseUnits) == 0 || len(out.PurchaseUnits[0].Payments.Captures) == 0 {
		return 0, "", fmt.Errorf("paypal khong co capture")
	}
	cap := out.PurchaseUnits[0].Payments.Captures[0]
	var amount float64
	_, _ = fmt.Sscanf(cap.Amount.Value, "%f", &amount)
	return amount, cap.ID, nil
}
