// main.go - storefront: web ban API Kiro-Go (landing + tai khoan khach + admin).
package main

import (
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// App gom cac phu thuoc dung chung (DB, template, client keyadmin, cau hinh).
type App struct {
	db            *DB
	tpl           *template.Template
	ka            *keyAdminClient
	adminPassword string
	secureCookie  bool
	usdToVND      int64
	bankInfo      BankInfo
	siteName      string
	baseURL       string // PUBLIC_BASE_URL cho oauth callback / paypal return
	checkKeyURL   string // URL trang check-key (keycheck sidecar / duong dan reverse-proxy)

	pay    PaymentConfig
	google GoogleOAuthConfig
}

// PaymentConfig gom cau hinh 3 cong thanh toan.
// Moi cong co cong tac bat/tat rieng (admin chinh) + thieu key thi cung coi nhu tat.
type PaymentConfig struct {
	// Cong tac bat/tat (admin chinh trong /frontadmin/settings)
	SepayOn  bool
	CryptoOn bool
	PaypalOn bool
	// SePay (tu dong qua webhook, VND)
	SepayAPIKey string
	// BEP20 USDT (Binance Smart Chain)
	BscScanAPIKey string
	USDTWallet    string // dia chi vi nhan USDT (BEP20)
	// PayPal (USD)
	PaypalClientID string
	PaypalSecret   string
	PaypalLive     bool // false = sandbox
}

func (p PaymentConfig) SepayEnabled() bool  { return p.SepayOn && p.SepayAPIKey != "" }
func (p PaymentConfig) CryptoEnabled() bool { return p.CryptoOn && p.USDTWallet != "" && p.BscScanAPIKey != "" }
func (p PaymentConfig) PaypalEnabled() bool {
	return p.PaypalOn && p.PaypalClientID != "" && p.PaypalSecret != ""
}

// GoogleOAuthConfig cau hinh dang nhap Google.
type GoogleOAuthConfig struct {
	ClientID     string
	ClientSecret string
}

func (g GoogleOAuthConfig) Enabled() bool { return g.ClientID != "" && g.ClientSecret != "" }

// BankInfo thong tin thanh toan hien o trang checkout.
type BankInfo struct {
	Bank    string
	Account string
	Holder  string
	Content string // cu phap noi dung chuyen khoan
	QRURL   string
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	dataDir := envOr("STOREFRONT_DATA_DIR", "./data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("khong tao duoc data dir: %v", err)
	}

	db, err := openDB(dataDir + "/storefront.db")
	if err != nil {
		log.Fatalf("khong mo duoc DB: %v", err)
	}
	go db.cleanupSessions()

	usd, _ := strconv.ParseInt(envOr("USD_TO_VND", "25000"), 10, 64)

	app := &App{
		db:            db,
		ka:            newKeyAdminClient(envOr("KEYADMIN_BASE", "http://keyadmin:8082"), os.Getenv("KEYADMIN_TOKEN")),
		adminPassword: os.Getenv("ADMIN_PASSWORD"),
		secureCookie:  envOr("SECURE_COOKIE", "true") != "false",
		usdToVND:      usd,
		siteName:      envOr("SITE_TITLE", "Kiro API Store"),
		baseURL:       strings.TrimRight(envOr("PUBLIC_BASE_URL", "http://localhost:8083"), "/"),
		// URL trang check-key. Local: keycheck sidecar (http://localhost:8081/check-key).
		// Production: dat CHECK_KEY_URL=/check-key (cung ten mien qua reverse-proxy).
		checkKeyURL:   envOr("CHECK_KEY_URL", "http://localhost:8081/check-key"),
		bankInfo: BankInfo{
			Bank:    envOr("BANK_NAME", "Vietcombank"),
			Account: envOr("BANK_ACCOUNT", "0123456789"),
			Holder:  envOr("BANK_HOLDER", "NGUYEN VAN A"),
			Content: envOr("BANK_CONTENT_PREFIX", "KIRO"),
			QRURL:   os.Getenv("BANK_QR_URL"),
		},
		pay: PaymentConfig{
			SepayAPIKey:    os.Getenv("SEPAY_API_KEY"),
			BscScanAPIKey:  os.Getenv("BSCSCAN_API_KEY"),
			USDTWallet:     strings.ToLower(os.Getenv("USDT_WALLET_ADDRESS")),
			PaypalClientID: os.Getenv("PAYPAL_CLIENT_ID"),
			PaypalSecret:   os.Getenv("PAYPAL_SECRET"),
			PaypalLive:     envOr("PAYPAL_LIVE", "false") == "true",
		},
		google: GoogleOAuthConfig{
			ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
			ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		},
	}

	// Nap cau hinh thanh toan tu DB (admin chinh), ghi de env neu co.
	app.loadPaymentConfig()

	if app.adminPassword == "" {
		log.Fatal("ADMIN_PASSWORD chua duoc dat - tu choi khoi dong")
	}
	if os.Getenv("KEYADMIN_TOKEN") == "" {
		log.Fatal("KEYADMIN_TOKEN chua duoc dat - tu choi khoi dong")
	}

	if err := app.loadTemplates(); err != nil {
		log.Fatalf("loi nap template: %v", err)
	}

	mux := http.NewServeMux()

	// Static assets
	fs := http.FileServer(http.Dir("./web/static"))
	mux.Handle("/static/", http.StripPrefix("/static/", fs))

	// Storefront cong khai
	mux.HandleFunc("/", app.handleLanding)
	mux.HandleFunc("/pricing", app.handlePricing)
	mux.HandleFunc("/register", app.handleRegister)
	mux.HandleFunc("/login", app.handleLogin)
	mux.HandleFunc("/logout", app.handleLogout)

	// Chuyen doi ngon ngu (vi/en)
	mux.HandleFunc("/lang", app.handleSetLang)

	// SEO
	mux.HandleFunc("/robots.txt", app.handleRobots)
	mux.HandleFunc("/sitemap.xml", app.handleSitemap)

	// Docs cong khai
	mux.HandleFunc("/docs", app.handleDocsIndex)
	mux.HandleFunc("/docs/", app.handleDocDetail)

	// Google OAuth
	mux.HandleFunc("/auth/google", app.handleGoogleLogin)
	mux.HandleFunc("/auth/google/callback", app.handleGoogleCallback)

	// Webhook SePay (tu dong xac nhan CK - khong can dang nhap, xac thuc bang API key).
	mux.HandleFunc("/webhook/sepay", app.handleSepayWebhook)

	// Khu vuc khach (yeu cau dang nhap)
	mux.HandleFunc("/dashboard", app.requireUser(app.handleDashboard))
	mux.HandleFunc("/checkout", app.requireUser(app.handleCheckout))
	mux.HandleFunc("/order/confirm", app.requireUser(app.handleOrderConfirm))
	mux.HandleFunc("/order/renew", app.requireUser(app.handleRenewOrder))
	mux.HandleFunc("/order/", app.requireUser(app.handleOrderDetail))
	// Thanh toan (khach)
	mux.HandleFunc("/pay/crypto/check", app.requireUser(app.handleCryptoCheck))
	mux.HandleFunc("/pay/paypal/capture", app.requireUser(app.handlePaypalCapture))

	// Admin storefront (duong dan /frontadmin de KHONG dung /admin cua kiro-go).
	mux.HandleFunc("/frontadmin/login", app.handleAdminLogin)
	mux.HandleFunc("/frontadmin", app.requireAdmin(app.handleAdminDashboard))
	mux.HandleFunc("/frontadmin/orders", app.requireAdmin(app.handleAdminOrders))
	mux.HandleFunc("/frontadmin/orders/action", app.requireAdmin(app.handleAdminOrderAction))
	mux.HandleFunc("/frontadmin/customers", app.requireAdmin(app.handleAdminCustomers))
	mux.HandleFunc("/frontadmin/keys", app.requireAdmin(app.handleAdminKeys))
	mux.HandleFunc("/frontadmin/keys/action", app.requireAdmin(app.handleAdminKeyAction))
	mux.HandleFunc("/frontadmin/packages", app.requireAdmin(app.handleAdminPackages))
	mux.HandleFunc("/frontadmin/packages/save", app.requireAdmin(app.handleAdminPackageSave))
	mux.HandleFunc("/frontadmin/settings", app.requireAdmin(app.handleAdminSettings))
	mux.HandleFunc("/frontadmin/docs", app.requireAdmin(app.handleAdminDocs))
	mux.HandleFunc("/frontadmin/docs/edit", app.requireAdmin(app.handleAdminDocEdit))
	mux.HandleFunc("/frontadmin/docs/save", app.requireAdmin(app.handleAdminDocSave))
	mux.HandleFunc("/frontadmin/docs/delete", app.requireAdmin(app.handleAdminDocDelete))

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	addr := envOr("STOREFRONT_LISTEN", ":8083")
	srv := &http.Server{
		Addr:              addr,
		Handler:           securityMiddleware(mux),
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}
	log.Printf("storefront listening on %s", addr)
	log.Fatal(srv.ListenAndServe())
}

// securityMiddleware them security headers co ban.
func securityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// vnd quy doi USD -> VND theo ti gia cau hinh.
func (app *App) vnd(usd float64) int64 {
	return int64(usd * float64(app.usdToVND))
}

// toFloat ep nhieu kieu so (int, int64, float64, ...) ve float64 cho template helper.
func toFloat(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case int32:
		return float64(n)
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	default:
		return 0
	}
}

// loadTemplates nap tat ca template .html voi ham helper.
func (app *App) loadTemplates() error {
	funcs := template.FuncMap{
		"t": t,
		"dict": func(values ...interface{}) map[string]interface{} {
			m := make(map[string]interface{}, len(values)/2)
			for i := 0; i+1 < len(values); i += 2 {
				key, _ := values[i].(string)
				m[key] = values[i+1]
			}
			return m
		},
		"money": func(v interface{}) string {
			return strconv.FormatFloat(toFloat(v), 'f', -1, 64)
		},
		"vnd": func(usd float64) string {
			n := int64(usd * float64(app.usdToVND))
			return formatVND(n)
		},
		"vndInt": formatVND,
		"comma":  formatVND,
		"upper":  strings.ToUpper,
		"pct": func(used, limit interface{}) int {
			u, l := toFloat(used), toFloat(limit)
			if l <= 0 {
				return 0
			}
			p := int(u / l * 100)
			if p > 100 {
				p = 100
			}
			return p
		},
	}
	t, err := template.New("").Funcs(funcs).ParseGlob("./web/templates/*.html")
	if err != nil {
		return err
	}
	app.tpl = t
	return nil
}

// formatVND dinh dang so nguyen kieu 1.234.567.
func formatVND(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, '.')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}
