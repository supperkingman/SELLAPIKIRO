// handlers_pay.go - endpoint khach tuong tac voi cong thanh toan crypto & paypal.
package main

import (
	"encoding/json"
	"net/http"
)

// handleCryptoCheck: khach bam "Toi da chuyen USDT" -> server quet BscScan.
// Tra ve JSON {approved: bool, message: string}.
func (app *App) handleCryptoCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	s := app.currentSession(r)
	code := r.FormValue("code")
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "thieu ma don"})
		return
	}
	// Xac nhan don thuoc ve khach nay.
	if !app.orderBelongsTo(code, s.UserID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "khong co quyen"})
		return
	}
	approved, err := app.checkCryptoPayment(code)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"approved": false,
			"message":  "Chưa tìm thấy giao dịch. Nếu đã chuyển, vui lòng đợi vài phút để mạng xác nhận rồi thử lại.",
		})
		return
	}
	if approved {
		writeJSON(w, http.StatusOK, map[string]interface{}{"approved": true, "message": "Thanh toán thành công! Key đã được tạo."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"approved": false,
		"message":  "Chưa nhận được đủ USDT. Kiểm tra lại số tiền và đợi mạng xác nhận (~1-2 phút).",
	})
}

// handlePaypalCapture: nhan paypal order id tu client sau khi approve -> capture server-side.
func (app *App) handlePaypalCapture(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	s := app.currentSession(r)
	var req struct {
		Code          string `json:"code"`
		PaypalOrderID string `json:"paypalOrderID"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if req.Code == "" || req.PaypalOrderID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "thieu tham so"})
		return
	}
	if !app.orderBelongsTo(req.Code, s.UserID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "khong co quyen"})
		return
	}

	// Lay gia USD cua don de doi chieu.
	var priceUSD float64
	if err := app.db.QueryRow(`SELECT price_usd FROM orders WHERE code=?`, req.Code).Scan(&priceUSD); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "don khong ton tai"})
		return
	}

	amount, captureID, err := app.capturePaypalOrder(req.PaypalOrderID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"approved": false, "message": "Không xác nhận được thanh toán PayPal: " + err.Error()})
		return
	}
	// Kiem tra so tien du (cho phep sai so nho 0.01).
	if amount+0.01 < priceUSD {
		writeJSON(w, http.StatusOK, map[string]interface{}{"approved": false, "message": "Số tiền thanh toán không đủ."})
		return
	}
	if err := app.fulfillOrder(req.Code, "paypal", "pp-"+captureID, amount, "USD"); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"approved": false, "message": "Lỗi tạo key: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"approved": true, "message": "Thanh toán thành công! Key đã được tạo."})
}

// orderBelongsTo kiem tra don co thuoc ve user khong.
func (app *App) orderBelongsTo(code string, userID int64) bool {
	var n int
	_ = app.db.QueryRow(`SELECT COUNT(*) FROM orders WHERE code=? AND user_id=?`, code, userID).Scan(&n)
	return n > 0
}
