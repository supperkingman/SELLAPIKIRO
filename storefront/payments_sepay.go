// payments_sepay.go - webhook SePay: tu dong xac nhan chuyen khoan VND.
// SePay goi POST toi day khi co tien vao tai khoan ngan hang lien ket.
// Doc: https://docs.sepay.vn/tich-hop-webhooks.html
package main

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// sepayWebhook la payload SePay gui (chi cac truong can dung).
type sepayWebhook struct {
	ID                 int64   `json:"id"`
	TransferType       string  `json:"transferType"` // "in" = tien vao
	TransferAmount     float64 `json:"transferAmount"`
	Content            string  `json:"content"`     // noi dung CK (chua ma don)
	ReferenceCode      string  `json:"referenceCode"`
	Description        string  `json:"description"`
}

// handleSepayWebhook xac thuc API key, match don theo ma trong noi dung, fulfill.
func (app *App) handleSepayWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Xac thuc: header "Authorization: Apikey <SEPAY_API_KEY>" (constant-time).
	if app.pay.SepayAPIKey == "" {
		http.Error(w, "sepay disabled", http.StatusServiceUnavailable)
		return
	}
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Apikey"))
	if subtle.ConstantTimeCompare([]byte(got), []byte(app.pay.SepayAPIKey)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]bool{"success": false})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var hook sepayWebhook
	if err := json.NewDecoder(r.Body).Decode(&hook); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]bool{"success": false})
		return
	}

	// Chi xu ly tien vao.
	if hook.TransferType != "in" {
		writeJSON(w, http.StatusOK, map[string]bool{"success": true})
		return
	}

	// Tim ma don trong noi dung CK (dang KIRO-XXXXXX).
	code := extractOrderCode(hook.Content + " " + hook.Description)
	if code == "" {
		log.Printf("sepay: khong tim thay ma don trong noi dung: %q", hook.Content)
		writeJSON(w, http.StatusOK, map[string]bool{"success": true}) // ack de sepay khong retry
		return
	}

	// Kiem tra so tien >= gia VND cua don.
	o, err := app.loadOrderVND(code)
	if err != nil {
		log.Printf("sepay: don %s khong ton tai", code)
		writeJSON(w, http.StatusOK, map[string]bool{"success": true})
		return
	}
	if hook.TransferAmount < float64(o.priceVND) {
		log.Printf("sepay: don %s thieu tien (nhan %.0f < %d)", code, hook.TransferAmount, o.priceVND)
		writeJSON(w, http.StatusOK, map[string]bool{"success": true})
		return
	}

	// Fulfill (ref = sepay tx id -> chong xu ly trung).
	ref := "sepay-" + itoa64(hook.ID)
	if err := app.fulfillOrder(code, "sepay", ref, hook.TransferAmount, "VND"); err != nil {
		log.Printf("sepay: fulfill don %s loi: %v", code, err)
		// van ack 200 de tranh retry lien tuc; se can admin kiem tra.
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// loadOrderVND lay gia VND cua don theo ma.
type orderVND struct{ priceVND int64 }

func (app *App) loadOrderVND(code string) (orderVND, error) {
	var o orderVND
	err := app.db.QueryRow(`SELECT price_vnd FROM orders WHERE code=?`, code).Scan(&o.priceVND)
	return o, err
}
