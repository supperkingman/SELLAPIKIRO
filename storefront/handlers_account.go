// handlers_account.go - khu vuc khach: dashboard, chi tiet don, key da mua.
package main

import (
	"net/http"
	"strings"
	"time"
)

// Order dai dien 1 don hang (kem ten goi).
type Order struct {
	ID          int64
	Code        string
	UserID      int64
	PackageID   int64
	PackageName string
	Credits     int
	PriceUSD    float64
	PriceVND    int64
	Status      string
	PaymentNote string
	CreatedAt   int64
	ApprovedAt  int64
	Email       string // dung o admin
}

// StatusLabel tra ve nhan tieng Viet cho trang thai don.
func (o Order) StatusLabel() string {
	switch o.Status {
	case "pending":
		return "Chờ thanh toán"
	case "reported":
		return "Chờ duyệt"
	case "approved":
		return "Đã giao key"
	case "cancelled":
		return "Đã hủy"
	}
	return o.Status
}

// CreatedTime dinh dang thoi gian tao.
func (o Order) CreatedTime() string {
	return time.Unix(o.CreatedAt, 0).Format("02/01/2006 15:04")
}

// IssuedKey mo ta key da giao cho khach.
type IssuedKey struct {
	ID          int64
	OrderCode   string
	KiroKeyID   string
	APIKey      string
	CreditLimit int
	CreatedAt   int64
	// usage runtime (tu keyadmin)
	CreditsUsed float64
	CreditsLeft float64
	Enabled     bool
	// han su dung (giai ma tu ten key)
	ExpiryMode  string // "none" | "active" | "paused"
	ExpiresAt   int64  // unix giay khi active
	SecondsLeft int64  // con lai bao nhieu giay
}

// listUserOrders lay don hang cua 1 khach.
func (app *App) listUserOrders(userID int64) ([]Order, error) {
	rows, err := app.db.Query(
		`SELECT o.id,o.code,o.package_id,p.name,o.credits,o.price_usd,o.price_vnd,o.status,o.payment_note,o.created_at,o.approved_at
		 FROM orders o JOIN packages p ON p.id=o.package_id
		 WHERE o.user_id=? ORDER BY o.created_at DESC`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Order
	for rows.Next() {
		var o Order
		if err := rows.Scan(&o.ID, &o.Code, &o.PackageID, &o.PackageName, &o.Credits, &o.PriceUSD, &o.PriceVND, &o.Status, &o.PaymentNote, &o.CreatedAt, &o.ApprovedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// listUserKeys lay key da giao cho khach, kem usage tu keyadmin (best-effort).
func (app *App) listUserKeys(userID int64) ([]IssuedKey, error) {
	rows, err := app.db.Query(
		`SELECT k.id,o.code,k.kiro_key_id,k.api_key,k.credit_limit,k.created_at
		 FROM issued_keys k JOIN orders o ON o.id=k.order_id
		 WHERE k.user_id=? ORDER BY k.created_at DESC`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IssuedKey
	for rows.Next() {
		var k IssuedKey
		if err := rows.Scan(&k.ID, &k.OrderCode, &k.KiroKeyID, &k.APIKey, &k.CreditLimit, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}

	// Enrich usage (neu keyadmin loi thi bo qua, van hien key)
	if usage, err := app.ka.usageByID(); err == nil {
		for i := range out {
			if u, ok := usage[out[i].KiroKeyID]; ok {
				out[i].CreditsUsed = u.CreditsUsed
				out[i].Enabled = u.Enabled
				left := u.CreditLimit - u.CreditsUsed
				if left < 0 {
					left = 0
				}
				out[i].CreditsLeft = left
				// Doc thang trang thai han keyadmin tinh san (u.Name da bi bo marker nen KHONG parse lai).
				out[i].ExpiryMode = u.Expiry.Mode
				out[i].ExpiresAt = u.Expiry.ExpiresAt
				out[i].SecondsLeft = u.Expiry.SecondsLeft
				if out[i].ExpiryMode == "" {
					out[i].ExpiryMode = "none"
				}
			}
		}
	}
	return out, nil
}

func (app *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	s := app.currentSession(r)
	orders, _ := app.listUserOrders(s.UserID)
	keys, _ := app.listUserKeys(s.UserID)
	pkgs, _ := app.listPackages(true) // goi active de chon khi gia han
	app.render(w, "dashboard.html", app.pageData(r, map[string]interface{}{
		"Orders":   orders,
		"Keys":     keys,
		"Packages": pkgs,
		"UsdToVND": app.usdToVND,
	}))
}

// handleOrderDetail hien chi tiet 1 don + huong dan thanh toan hoac key da giao.
func (app *App) handleOrderDetail(w http.ResponseWriter, r *http.Request) {
	// Auto-polling goi /order/<code>/status -> tra JSON trang thai.
	if strings.HasSuffix(r.URL.Path, "/status") {
		app.handleOrderStatus(w, r)
		return
	}
	s := app.currentSession(r)
	code := strings.TrimPrefix(r.URL.Path, "/order/")
	if code == "" {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	var o Order
	var payAmount float64
	err := app.db.QueryRow(
		`SELECT o.id,o.code,o.package_id,p.name,o.credits,o.price_usd,o.price_vnd,o.status,o.payment_note,o.created_at,o.approved_at,o.pay_amount
		 FROM orders o JOIN packages p ON p.id=o.package_id
		 WHERE o.code=? AND o.user_id=?`, code, s.UserID,
	).Scan(&o.ID, &o.Code, &o.PackageID, &o.PackageName, &o.Credits, &o.PriceUSD, &o.PriceVND, &o.Status, &o.PaymentNote, &o.CreatedAt, &o.ApprovedAt, &payAmount)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Neu da giao key -> lay key cua don nay
	var key *IssuedKey
	if o.Status == "approved" {
		var k IssuedKey
		if err := app.db.QueryRow(
			`SELECT id,kiro_key_id,api_key,credit_limit,created_at FROM issued_keys WHERE order_id=?`, o.ID,
		).Scan(&k.ID, &k.KiroKeyID, &k.APIKey, &k.CreditLimit, &k.CreatedAt); err == nil {
			key = &k
		}
	}

	app.render(w, "order.html", app.pageData(r, map[string]interface{}{
		"Order":         o,
		"Key":           key,
		"Bank":          app.bankInfo,
		"PayAmount":     payAmount,
		"Pay":           app.pay,
		"USDTWallet":    app.pay.USDTWallet,
		"PaypalID":      app.pay.PaypalClientID,
		"SepayEnabled":  app.pay.SepayEnabled(),
		"CryptoEnabled": app.pay.CryptoEnabled(),
		"PaypalEnabled": app.pay.PaypalEnabled(),
	}))
}
