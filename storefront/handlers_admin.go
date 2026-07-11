// handlers_admin.go - khu vuc quan tri (Shopify Polaris-style).
package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (app *App) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if s := app.currentSession(r); s != nil && s.IsAdmin {
		http.Redirect(w, r, "/frontadmin", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodPost {
		pw := r.FormValue("password")
		if !app.adminLogin(pw) {
			app.render(w, "admin_login.html", app.pageData(r, map[string]interface{}{
				"Error": "Mật khẩu quản trị không đúng.",
			}))
			return
		}
		// Dung 1 user he thong (id=0 khong ton tai trong users) -> luu session admin
		// bang cach tao ban ghi session gan voi userID=0. Vi FK, ta dung user admin ao.
		app.ensureAdminUser()
		_ = app.createSession(w, adminUserID, true)
		http.Redirect(w, r, "/frontadmin", http.StatusSeeOther)
		return
	}
	app.render(w, "admin_login.html", app.pageData(r, nil))
}

const adminUserID = 1000000000 // id danh rieng cho admin ao

// ensureAdminUser tao 1 ban ghi user admin ao (idempotent) de session co FK hop le.
func (app *App) ensureAdminUser() {
	_, _ = app.db.Exec(
		`INSERT OR IGNORE INTO users(id,email,password_hash,full_name,created_at) VALUES(?,?,?,?,?)`,
		adminUserID, "admin@system.local", "-", "Administrator", time.Now().Unix(),
	)
}

// adminStats bo dem tong quan cho dashboard.
type adminStats struct {
	TotalCustomers int
	TotalOrders    int
	PendingOrders  int
	KeysIssued     int
	RevenueUSD     float64
	RevenueVND     int64
}

func (app *App) computeStats() adminStats {
	var s adminStats
	_ = app.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id<>?`, adminUserID).Scan(&s.TotalCustomers)
	_ = app.db.QueryRow(`SELECT COUNT(*) FROM orders`).Scan(&s.TotalOrders)
	_ = app.db.QueryRow(`SELECT COUNT(*) FROM orders WHERE status IN ('pending','reported')`).Scan(&s.PendingOrders)
	_ = app.db.QueryRow(`SELECT COUNT(*) FROM issued_keys`).Scan(&s.KeysIssued)
	_ = app.db.QueryRow(`SELECT COALESCE(SUM(price_usd),0), COALESCE(SUM(price_vnd),0) FROM orders WHERE status='approved'`).Scan(&s.RevenueUSD, &s.RevenueVND)
	return s
}

func (app *App) handleAdminDashboard(w http.ResponseWriter, r *http.Request) {
	stats := app.computeStats()
	recent, _ := app.listAllOrders("", 8)
	app.render(w, "admin_dashboard.html", app.adminPage(r, map[string]interface{}{
		"Stats":  stats,
		"Recent": recent,
		"Active": "dashboard",
	}))
}

// adminPage them cac field chung cho template admin.
func (app *App) adminPage(r *http.Request, extra map[string]interface{}) map[string]interface{} {
	return app.pageData(r, extra)
}

// listAllOrders lay don hang (loc theo status neu co), gioi han limit (0 = tat ca).
func (app *App) listAllOrders(status string, limit int) ([]Order, error) {
	q := `SELECT o.id,o.code,o.user_id,u.email,o.package_id,p.name,o.credits,o.price_usd,o.price_vnd,o.status,o.payment_note,o.created_at,o.approved_at
		  FROM orders o JOIN packages p ON p.id=o.package_id JOIN users u ON u.id=o.user_id`
	args := []interface{}{}
	if status != "" {
		q += ` WHERE o.status=?`
		args = append(args, status)
	}
	q += ` ORDER BY o.created_at DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := app.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Order
	for rows.Next() {
		var o Order
		if err := rows.Scan(&o.ID, &o.Code, &o.UserID, &o.Email, &o.PackageID, &o.PackageName, &o.Credits, &o.PriceUSD, &o.PriceVND, &o.Status, &o.PaymentNote, &o.CreatedAt, &o.ApprovedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

func (app *App) handleAdminOrders(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	orders, _ := app.listAllOrders(status, 0)
	app.render(w, "admin_orders.html", app.adminPage(r, map[string]interface{}{
		"Orders": orders,
		"Filter": status,
		"Active": "orders",
	}))
}

// handleAdminOrderAction: duyet (approve) hoac huy (cancel) don.
// Duyet -> goi keyadmin tao key -> luu issued_keys -> chuyen don sang approved.
func (app *App) handleAdminOrderAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/frontadmin/orders", http.StatusSeeOther)
		return
	}
	code := r.FormValue("code")
	action := r.FormValue("action")

	var o Order
	var durDays, durHours int
	err := app.db.QueryRow(
		`SELECT o.id,o.code,o.user_id,u.email,o.package_id,p.name,o.credits,o.status,p.duration_days,p.duration_hours
		 FROM orders o JOIN packages p ON p.id=o.package_id JOIN users u ON u.id=o.user_id
		 WHERE o.code=?`, code,
	).Scan(&o.ID, &o.Code, &o.UserID, &o.Email, &o.PackageID, &o.PackageName, &o.Credits, &o.Status, &durDays, &durHours)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	switch action {
	case "cancel":
		_, _ = app.db.Exec(`UPDATE orders SET status='cancelled' WHERE id=?`, o.ID)
	case "approve":
		if o.Status == "approved" {
			http.Redirect(w, r, "/frontadmin/orders", http.StatusSeeOther)
			return
		}
		// Ten key trong kiro-go: de admin de tra cuu.
		keyName := fmt.Sprintf("%s | %s | %s", o.Code, o.PackageName, o.Email)
		created, err := app.ka.CreateKeyWithExpiry(keyName, o.Credits, durDays, durHours)
		if err != nil {
			app.render(w, "admin_error.html", app.adminPage(r, map[string]interface{}{
				"Message": "Không tạo được key qua keyadmin: " + err.Error(),
				"Active":  "orders",
			}))
			return
		}
		// keyadmin khong tra ve id -> tra cuu id qua danh sach key theo TEN (duy nhat vi co ma don).
		kiroID := app.lookupKeyID(created.Name)
		_, err = app.db.Exec(
			`INSERT INTO issued_keys(order_id,user_id,kiro_key_id,api_key,credit_limit,created_at)
			 VALUES(?,?,?,?,?,?)`,
			o.ID, o.UserID, kiroID, created.Key, o.Credits, time.Now().Unix(),
		)
		if err != nil {
			http.Error(w, "loi luu key: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = app.db.Exec(`UPDATE orders SET status='approved', approved_at=? WHERE id=?`, time.Now().Unix(), o.ID)
	}
	http.Redirect(w, r, "/frontadmin/orders", http.StatusSeeOther)
}

// lookupKeyID tim id cua key trong kiro-go dua tren TEN key (da bo marker).
// keyadmin /api/keys khong tra ve gia tri key, nhung tra ve name -> match theo name.
func (app *App) lookupKeyID(name string) string {
	keys, err := app.ka.ListKeys()
	if err != nil {
		return ""
	}
	for _, k := range keys {
		if k.Name == name {
			return k.ID
		}
	}
	return ""
}

func (app *App) handleAdminCustomers(w http.ResponseWriter, r *http.Request) {
	rows, _ := app.db.Query(
		`SELECT u.id,u.email,u.full_name,u.created_at,u.is_blocked,
		 (SELECT COUNT(*) FROM orders o WHERE o.user_id=u.id) as norders,
		 (SELECT COUNT(*) FROM issued_keys k WHERE k.user_id=u.id) as nkeys
		 FROM users u WHERE u.id<>? ORDER BY u.created_at DESC`, adminUserID,
	)
	type customer struct {
		ID        int64
		Email     string
		FullName  string
		CreatedAt int64
		IsBlocked bool
		NOrders   int
		NKeys     int
	}
	var custs []customer
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var c customer
			_ = rows.Scan(&c.ID, &c.Email, &c.FullName, &c.CreatedAt, &c.IsBlocked, &c.NOrders, &c.NKeys)
			custs = append(custs, c)
		}
	}
	app.render(w, "admin_customers.html", app.adminPage(r, map[string]interface{}{
		"Customers": custs,
		"Active":    "customers",
	}))
}

// adminKeyRow: 1 dong key trong bang quan tri (kem usage + han tu keyadmin).
type adminKeyRow struct {
	ID          int64
	OrderCode   string
	Email       string
	APIKey      string
	KiroKeyID   string
	CreditLimit int
	CreatedAt   int64
	// runtime tu keyadmin
	CreditsUsed float64
	CreditsLeft float64
	Enabled     bool
	ExpiryMode  string // none | active | paused
	ExpiresAt   int64
	SecondsLeft int64
}

func (app *App) handleAdminKeys(w http.ResponseWriter, r *http.Request) {
	rows, _ := app.db.Query(
		`SELECT k.id,o.code,u.email,k.api_key,k.kiro_key_id,k.credit_limit,k.created_at
		 FROM issued_keys k JOIN orders o ON o.id=k.order_id JOIN users u ON u.id=k.user_id
		 ORDER BY k.created_at DESC`,
	)
	var keys []adminKeyRow
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var k adminKeyRow
			_ = rows.Scan(&k.ID, &k.OrderCode, &k.Email, &k.APIKey, &k.KiroKeyID, &k.CreditLimit, &k.CreatedAt)
			keys = append(keys, k)
		}
	}
	// Enrich usage + han tu keyadmin (best-effort).
	if usage, err := app.ka.usageByID(); err == nil {
		for i := range keys {
			if u, ok := usage[keys[i].KiroKeyID]; ok {
				keys[i].CreditsUsed = u.CreditsUsed
				keys[i].Enabled = u.Enabled
				left := u.CreditLimit - u.CreditsUsed
				if left < 0 {
					left = 0
				}
				keys[i].CreditsLeft = left
				// Doc thang trang thai han keyadmin tinh san (u.Name da bi bo marker nen KHONG parse lai).
				keys[i].ExpiryMode = u.Expiry.Mode
				keys[i].ExpiresAt = u.Expiry.ExpiresAt
				keys[i].SecondsLeft = u.Expiry.SecondsLeft
				if keys[i].ExpiryMode == "" {
					keys[i].ExpiryMode = "none"
				}
			}
		}
	}
	app.render(w, "admin_keys.html", app.adminPage(r, map[string]interface{}{
		"Keys":   keys,
		"Active": "keys",
	}))
}

// handleAdminKeyAction xu ly cac thao tac tren 1 key da giao:
// add_credit / sub_credit / add_time / sub_time / rename / delete / renew.
func (app *App) handleAdminKeyAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/frontadmin/keys", http.StatusSeeOther)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	action := r.FormValue("action")

	// Lay key + kiro_key_id tu DB.
	var kiroID string
	var creditLimit int
	if err := app.db.QueryRow(
		`SELECT kiro_key_id, credit_limit FROM issued_keys WHERE id=?`, id,
	).Scan(&kiroID, &creditLimit); err != nil {
		http.NotFound(w, r)
		return
	}

	fail := func(msg string) {
		app.render(w, "admin_error.html", app.adminPage(r, map[string]interface{}{
			"Message": msg, "Active": "keys",
		}))
	}

	switch action {
	case "add_credit", "sub_credit":
		amt, _ := strconv.ParseFloat(r.FormValue("amount"), 64)
		if action == "sub_credit" {
			amt = -amt
		}
		if kiroID == "" {
			fail("Key chưa liên kết với kiro-go (thiếu kiro_key_id).")
			return
		}
		if err := app.ka.AddCredit(kiroID, amt); err != nil {
			fail("Không đổi được credit: " + err.Error())
			return
		}
		newLimit := creditLimit + int(amt)
		if newLimit < 0 {
			newLimit = 0
		}
		_, _ = app.db.Exec(`UPDATE issued_keys SET credit_limit=? WHERE id=?`, newLimit, id)

	case "add_time", "sub_time":
		days, _ := strconv.Atoi(r.FormValue("days"))
		hours, _ := strconv.Atoi(r.FormValue("hours"))
		total := days*24 + hours
		if action == "sub_time" {
			total = -total
		}
		if kiroID == "" {
			fail("Key chưa liên kết với kiro-go (thiếu kiro_key_id).")
			return
		}
		if err := app.ka.AddHours(kiroID, total); err != nil {
			fail("Không đổi được thời hạn: " + err.Error())
			return
		}

	case "set_time":
		// Dat thoi han tuyet doi = now + days + hours (0/0 = vinh vien).
		days, _ := strconv.Atoi(r.FormValue("days"))
		hours, _ := strconv.Atoi(r.FormValue("hours"))
		if kiroID == "" {
			fail("Key chưa liên kết với kiro-go (thiếu kiro_key_id).")
			return
		}
		if err := app.ka.SetExpiry(kiroID, days, hours); err != nil {
			fail("Không đặt được thời hạn: " + err.Error())
			return
		}

	case "set_credit":
		// Dat credit tuyet doi.
		amt, _ := strconv.ParseFloat(r.FormValue("amount"), 64)
		if kiroID == "" {
			fail("Key chưa liên kết với kiro-go (thiếu kiro_key_id).")
			return
		}
		if err := app.ka.SetCredit(kiroID, amt); err != nil {
			fail("Không đặt được credit: " + err.Error())
			return
		}
		newLimit := int(amt)
		if newLimit < 0 {
			newLimit = 0
		}
		_, _ = app.db.Exec(`UPDATE issued_keys SET credit_limit=? WHERE id=?`, newLimit, id)

	case "rename":
		name := strings.TrimSpace(r.FormValue("name"))
		if kiroID == "" || name == "" {
			fail("Thiếu tên mới hoặc key chưa liên kết kiro-go.")
			return
		}
		if err := app.ka.RenameKey(kiroID, name); err != nil {
			fail("Không đổi được tên: " + err.Error())
			return
		}

	case "delete":
		if kiroID != "" {
			if err := app.ka.DeleteKey(kiroID); err != nil {
				fail("Không xóa được key trên kiro-go: " + err.Error())
				return
			}
		}
		_, _ = app.db.Exec(`DELETE FROM issued_keys WHERE id=?`, id)

	case "renew":
		// Gia han truc tiep: cong credit + thoi gian nhap tay vao key.
		amt, _ := strconv.ParseFloat(r.FormValue("amount"), 64)
		days, _ := strconv.Atoi(r.FormValue("days"))
		hours, _ := strconv.Atoi(r.FormValue("hours"))
		if kiroID == "" {
			fail("Key chưa liên kết với kiro-go (thiếu kiro_key_id).")
			return
		}
		if amt != 0 {
			if err := app.ka.AddCredit(kiroID, amt); err != nil {
				fail("Không cộng được credit: " + err.Error())
				return
			}
			newLimit := creditLimit + int(amt)
			if newLimit < 0 {
				newLimit = 0
			}
			_, _ = app.db.Exec(`UPDATE issued_keys SET credit_limit=? WHERE id=?`, newLimit, id)
		}
		if days != 0 || hours != 0 {
			if err := app.ka.AddHours(kiroID, days*24+hours); err != nil {
				fail("Không cộng được thời gian: " + err.Error())
				return
			}
		}
	}
	http.Redirect(w, r, "/frontadmin/keys", http.StatusSeeOther)
}

func (app *App) handleAdminPackages(w http.ResponseWriter, r *http.Request) {
	pkgs, _ := app.listPackages(false)
	app.render(w, "admin_packages.html", app.adminPage(r, map[string]interface{}{
		"Packages": pkgs,
		"Active":   "packages",
	}))
}

func (app *App) handleAdminPackageSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/frontadmin/packages", http.StatusSeeOther)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	credits, _ := strconv.Atoi(r.FormValue("credits"))
	orig, _ := strconv.ParseFloat(r.FormValue("original_price"), 64)
	price, _ := strconv.ParseFloat(r.FormValue("price"), 64)
	durDays, _ := strconv.Atoi(r.FormValue("duration_days"))
	durHours, _ := strconv.Atoi(r.FormValue("duration_hours"))
	active := 0
	if r.FormValue("is_active") == "on" {
		active = 1
	}
	_, _ = app.db.Exec(
		`UPDATE packages SET name=?,credits=?,original_price_usd=?,price_usd=?,is_active=?,duration_days=?,duration_hours=? WHERE id=?`,
		r.FormValue("name"), credits, orig, price, active, durDays, durHours, id,
	)
	http.Redirect(w, r, "/frontadmin/packages", http.StatusSeeOther)
}

func (app *App) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		// Thong tin ngan hang
		_ = app.db.setSetting("bank_name", r.FormValue("bank_name"))
		_ = app.db.setSetting("bank_account", r.FormValue("bank_account"))
		_ = app.db.setSetting("bank_holder", r.FormValue("bank_holder"))
		app.bankInfo.Bank = r.FormValue("bank_name")
		app.bankInfo.Account = r.FormValue("bank_account")
		app.bankInfo.Holder = r.FormValue("bank_holder")

		// Cong tac bat/tat cong thanh toan
		_ = app.db.setSetting("pay_sepay_on", checkboxVal(r.FormValue("pay_sepay_on")))
		_ = app.db.setSetting("pay_crypto_on", checkboxVal(r.FormValue("pay_crypto_on")))
		_ = app.db.setSetting("pay_paypal_on", checkboxVal(r.FormValue("pay_paypal_on")))

		// API/credential tung cong (chi ghi de khi co nhap, tranh xoa nham gia tri cu)
		if v := strings.TrimSpace(r.FormValue("sepay_api_key")); v != "" {
			_ = app.db.setSetting("sepay_api_key", v)
		}
		if v := strings.TrimSpace(r.FormValue("bscscan_api_key")); v != "" {
			_ = app.db.setSetting("bscscan_api_key", v)
		}
		if v := strings.TrimSpace(r.FormValue("usdt_wallet")); v != "" {
			_ = app.db.setSetting("usdt_wallet", strings.ToLower(v))
		}
		if v := strings.TrimSpace(r.FormValue("paypal_client_id")); v != "" {
			_ = app.db.setSetting("paypal_client_id", v)
		}
		if v := strings.TrimSpace(r.FormValue("paypal_secret")); v != "" {
			_ = app.db.setSetting("paypal_secret", v)
		}
		_ = app.db.setSetting("paypal_live", checkboxVal(r.FormValue("paypal_live")))

		app.loadPaymentConfig()
		http.Redirect(w, r, "/frontadmin/settings", http.StatusSeeOther)
		return
	}
	app.render(w, "admin_settings.html", app.adminPage(r, map[string]interface{}{
		"Bank":   app.bankInfo,
		"Pay":    app.pay,
		"Active": "settings",
	}))
}

// checkboxVal chuyen gia tri checkbox ("on"/"") thanh "1"/"0".
func checkboxVal(v string) string {
	if v == "on" || v == "1" || v == "true" {
		return "1"
	}
	return "0"
}

// loadPaymentConfig nap cau hinh thanh toan tu DB (uu tien DB, fallback env).
func (app *App) loadPaymentConfig() {
	getDef := func(key, def string) string { return app.db.getSetting(key, def) }
	// Toggle: mac dinh bat neu chua tung luu (de tuong thich cu).
	app.pay.SepayOn = getDef("pay_sepay_on", "1") == "1"
	app.pay.CryptoOn = getDef("pay_crypto_on", "1") == "1"
	app.pay.PaypalOn = getDef("pay_paypal_on", "1") == "1"
	// Credential: DB > env hien tai.
	app.pay.SepayAPIKey = getDef("sepay_api_key", app.pay.SepayAPIKey)
	app.pay.BscScanAPIKey = getDef("bscscan_api_key", app.pay.BscScanAPIKey)
	app.pay.USDTWallet = strings.ToLower(getDef("usdt_wallet", app.pay.USDTWallet))
	app.pay.PaypalClientID = getDef("paypal_client_id", app.pay.PaypalClientID)
	app.pay.PaypalSecret = getDef("paypal_secret", app.pay.PaypalSecret)
	app.pay.PaypalLive = getDef("paypal_live", boolToStr(app.pay.PaypalLive)) == "1"
}

// boolToStr chuyen bool sang "1"/"0" cho getSetting default.
func boolToStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
