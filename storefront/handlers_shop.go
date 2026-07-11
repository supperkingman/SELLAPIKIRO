// handlers_shop.go - trang cong khai + luong mua (checkout).
package main

import (
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Package mo ta 1 goi ban.
type Package struct {
	ID            int64
	Slug          string
	Name          string
	Credits       int
	OrigPrice     float64
	Price         float64
	SortOrder     int
	IsActive      bool
	IsFeatured    bool
	DurationDays  int
	DurationHours int
}

// Discount % giam so voi gia goc.
func (p Package) Discount() int {
	if p.OrigPrice <= 0 {
		return 0
	}
	return int((1 - p.Price/p.OrigPrice) * 100)
}

// listPackages lay cac goi dang active (sap theo sort_order).
func (app *App) listPackages(onlyActive bool) ([]Package, error) {
	q := `SELECT id,slug,name,credits,original_price_usd,price_usd,sort_order,is_active,is_featured,duration_days,duration_hours FROM packages`
	if onlyActive {
		q += ` WHERE is_active=1`
	}
	q += ` ORDER BY sort_order ASC`
	rows, err := app.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Package
	for rows.Next() {
		var p Package
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.Credits, &p.OrigPrice, &p.Price, &p.SortOrder, &p.IsActive, &p.IsFeatured, &p.DurationDays, &p.DurationHours); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (app *App) findPackage(id int64) (Package, error) {
	var p Package
	err := app.db.QueryRow(
		`SELECT id,slug,name,credits,original_price_usd,price_usd,sort_order,is_active,is_featured,duration_days,duration_hours FROM packages WHERE id=?`, id,
	).Scan(&p.ID, &p.Slug, &p.Name, &p.Credits, &p.OrigPrice, &p.Price, &p.SortOrder, &p.IsActive, &p.IsFeatured, &p.DurationDays, &p.DurationHours)
	return p, err
}

// pageData gom du lieu chung cho template.
func (app *App) pageData(r *http.Request, extra map[string]interface{}) map[string]interface{} {
	d := map[string]interface{}{
		"SiteName": app.siteName,
		"Session":  app.currentSession(r),
		"Year":     time.Now().Year(),
		"Lang":     currentLang(r),
		"BaseURL":     app.baseURL,
		"Path":        r.URL.Path,
		"CheckKeyURL": app.checkKeyURL,
	}
	for k, v := range extra {
		d[k] = v
	}
	return d
}

func (app *App) render(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := app.tpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "render error: "+err.Error(), http.StatusInternalServerError)
	}
}

func (app *App) handleLanding(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	pkgs, _ := app.listPackages(true)
	app.render(w, "landing.html", app.pageData(r, map[string]interface{}{
		"Packages": pkgs,
		"UsdToVND": app.usdToVND,
	}))
}

func (app *App) handlePricing(w http.ResponseWriter, r *http.Request) {
	pkgs, _ := app.listPackages(true)
	app.render(w, "pricing.html", app.pageData(r, map[string]interface{}{
		"Packages": pkgs,
	}))
}

func (app *App) handleRegister(w http.ResponseWriter, r *http.Request) {
	if app.currentSession(r) != nil {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodPost {
		email := strings.TrimSpace(r.FormValue("email"))
		pw := r.FormValue("password")
		name := r.FormValue("full_name")
		if len(pw) < 6 || !strings.Contains(email, "@") {
			app.render(w, "register.html", app.pageData(r, map[string]interface{}{
				"Error": "Email không hợp lệ hoặc mật khẩu dưới 6 ký tự.", "Email": email,
			}))
			return
		}
		id, err := app.registerUser(email, pw, name)
		if err != nil {
			app.render(w, "register.html", app.pageData(r, map[string]interface{}{
				"Error": "Email này đã được đăng ký.", "Email": email,
			}))
			return
		}
		_ = app.createSession(w, id, false)
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	app.render(w, "register.html", app.pageData(r, nil))
}

func (app *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if app.currentSession(r) != nil {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodPost {
		email := r.FormValue("email")
		pw := r.FormValue("password")
		u, ok := app.authenticate(email, pw)
		if !ok {
			app.render(w, "login.html", app.pageData(r, map[string]interface{}{
				"Error": "Email hoặc mật khẩu không đúng.", "Email": email,
			}))
			return
		}
		_ = app.createSession(w, u.ID, false)
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	app.render(w, "login.html", app.pageData(r, nil))
}

func (app *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	app.destroySession(w, r)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleCheckout: GET hien trang xac nhan mua + thong tin CK; tao don o trang thai pending.
func (app *App) handleCheckout(w http.ResponseWriter, r *http.Request) {
	s := app.currentSession(r)
	pkgID, _ := strconv.ParseInt(r.URL.Query().Get("package"), 10, 64)
	if r.Method == http.MethodPost {
		pkgID, _ = strconv.ParseInt(r.FormValue("package_id"), 10, 64)
	}
	pkg, err := app.findPackage(pkgID)
	if err != nil || !pkg.IsActive {
		http.Redirect(w, r, "/pricing", http.StatusSeeOther)
		return
	}

	if r.Method == http.MethodPost {
		code := genOrderCode()
		priceVND := app.vnd(pkg.Price)
		// So tien USDT le duy nhat de phan biet cac don cung gia (vd 3.00, 3.01...).
		payAmount := app.uniqueUSDTAmount(pkg.Price)
		_, err := app.db.Exec(
			`INSERT INTO orders(code,user_id,package_id,credits,price_usd,price_vnd,status,created_at,pay_amount)
			 VALUES(?,?,?,?,?,?, 'pending', ?, ?)`,
			code, s.UserID, pkg.ID, pkg.Credits, pkg.Price, priceVND, time.Now().Unix(), payAmount,
		)
		if err != nil {
			http.Error(w, "khong tao duoc don hang", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/order/"+code, http.StatusSeeOther)
		return
	}

	app.render(w, "checkout.html", app.pageData(r, map[string]interface{}{
		"Pkg":      pkg,
		"PriceVND": app.vnd(pkg.Price),
		"Bank":     app.bankInfo,
	}))
}

// uniqueUSDTAmount tim so tien USDT le (base + 0.00..0.99) chua bi don pending
// nao khac dung, de match giao dich on-chain khong nham lan.
func (app *App) uniqueUSDTAmount(base float64) float64 {
	for cents := 0; cents < 100; cents++ {
		amt := base + float64(cents)/100.0
		var n int
		_ = app.db.QueryRow(
			`SELECT COUNT(*) FROM orders WHERE pay_amount=? AND status IN ('pending','reported')`, amt,
		).Scan(&n)
		if n == 0 {
			return amt
		}
	}
	return base // hiem khi het, fallback
}

// handleRenewOrder: khach bam "Gia han" tren 1 key -> chon goi -> tao don gia han.
// Don gia han gan renew_key_id = issued_keys.id. Khi thanh toan xac nhan (fulfillOrder),
// he thong CONG credit + thoi gian cua goi vao key cu thay vi tao key moi.
func (app *App) handleRenewOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	s := app.currentSession(r)
	keyID, _ := strconv.ParseInt(r.FormValue("key_id"), 10, 64)
	pkgID, _ := strconv.ParseInt(r.FormValue("package_id"), 10, 64)

	// Xac nhan key thuoc ve khach nay.
	var ownerID int64
	if err := app.db.QueryRow(
		`SELECT user_id FROM issued_keys WHERE id=?`, keyID,
	).Scan(&ownerID); err != nil || ownerID != s.UserID {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	pkg, err := app.findPackage(pkgID)
	if err != nil || !pkg.IsActive {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	code := genOrderCode()
	priceVND := app.vnd(pkg.Price)
	payAmount := app.uniqueUSDTAmount(pkg.Price)
	_, err = app.db.Exec(
		`INSERT INTO orders(code,user_id,package_id,credits,price_usd,price_vnd,status,created_at,pay_amount,renew_key_id)
		 VALUES(?,?,?,?,?,?, 'pending', ?, ?, ?)`,
		code, s.UserID, pkg.ID, pkg.Credits, pkg.Price, priceVND, time.Now().Unix(), payAmount, keyID,
	)
	if err != nil {
		http.Error(w, "khong tao duoc don gia han", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/order/"+code, http.StatusSeeOther)
}

// handleOrderConfirm: khach bam "toi da chuyen khoan" -> danh dau da bao (van pending, cho admin duyet).
func (app *App) handleOrderConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	s := app.currentSession(r)
	code := r.FormValue("code")
	note := r.FormValue("note")
	_, _ = app.db.Exec(
		`UPDATE orders SET payment_note=?, status='reported' WHERE code=? AND user_id=? AND status='pending'`,
		note, code, s.UserID,
	)
	http.Redirect(w, r, "/order/"+code, http.StatusSeeOther)
}

// handleOrderStatus: tra ve trang thai don duoi dang JSON, phuc vu auto-polling o trang don.
func (app *App) handleOrderStatus(w http.ResponseWriter, r *http.Request) {
	s := app.currentSession(r)
	// URL dang /order/<code>/status
	path := strings.TrimPrefix(r.URL.Path, "/order/")
	code := strings.TrimSuffix(path, "/status")
	code = strings.Trim(code, "/")
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "thieu ma don"})
		return
	}
	var status string
	err := app.db.QueryRow(
		`SELECT status FROM orders WHERE code=? AND user_id=?`, code, s.UserID,
	).Scan(&status)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "khong tim thay don"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

// genOrderCode sinh ma don hang dang KIRO-XXXXXX.
func genOrderCode() string {
	const charset = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 6)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return fmt.Sprintf("KIRO-%s", string(b))
}
