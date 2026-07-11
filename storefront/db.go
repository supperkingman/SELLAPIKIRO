// db.go - khoi tao SQLite (thuan Go, khong CGO) va schema cho storefront.
package main

import (
	"database/sql"
	"log"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DB bao boc *sql.DB de gan cac helper truy van.
type DB struct{ *sql.DB }

// openDB mo (hoac tao) file SQLite va bat cac pragma an toan/hieu nang.
func openDB(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1) // SQLite ghi don luong -> tranh "database is locked"
	if err := sqlDB.Ping(); err != nil {
		return nil, err
	}
	db := &DB{sqlDB}
	if err := db.migrate(); err != nil {
		return nil, err
	}
	if err := db.seedPackages(); err != nil {
		return nil, err
	}
	return db, nil
}

// migrate tao cac bang neu chua ton tai.
func (db *DB) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			full_name TEXT DEFAULT '',
			created_at INTEGER NOT NULL,
			is_blocked INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL,
			is_admin INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS packages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			slug TEXT UNIQUE NOT NULL,
			name TEXT NOT NULL,
			credits INTEGER NOT NULL,
			original_price_usd REAL NOT NULL,
			price_usd REAL NOT NULL,
			sort_order INTEGER NOT NULL DEFAULT 0,
			is_active INTEGER NOT NULL DEFAULT 1,
			is_featured INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS orders (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			code TEXT UNIQUE NOT NULL,
			user_id INTEGER NOT NULL,
			package_id INTEGER NOT NULL,
			credits INTEGER NOT NULL,
			price_usd REAL NOT NULL,
			price_vnd INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			payment_note TEXT DEFAULT '',
			created_at INTEGER NOT NULL,
			approved_at INTEGER DEFAULT 0,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY(package_id) REFERENCES packages(id)
		)`,
		`CREATE TABLE IF NOT EXISTS issued_keys (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			order_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			kiro_key_id TEXT DEFAULT '',
			api_key TEXT NOT NULL,
			credit_limit INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			FOREIGN KEY(order_id) REFERENCES orders(id) ON DELETE CASCADE,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		// payments: 1 don co the co nhieu lan thu thanh toan qua cac cong khac nhau.
		`CREATE TABLE IF NOT EXISTS payments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			order_id INTEGER NOT NULL,
			method TEXT NOT NULL,              -- sepay | crypto | paypal
			amount REAL NOT NULL,              -- so tien theo don vi cua cong (VND cho sepay, USD cho crypto/paypal)
			currency TEXT NOT NULL DEFAULT 'USD',
			ref TEXT DEFAULT '',               -- tx_hash (crypto) | paypal_order_id | sepay_id
			status TEXT NOT NULL DEFAULT 'pending', -- pending | confirmed | failed
			created_at INTEGER NOT NULL,
			confirmed_at INTEGER DEFAULT 0,
			FOREIGN KEY(order_id) REFERENCES orders(id) ON DELETE CASCADE
		)`,
		// Chong dung lai cung 1 giao dich on-chain / paypal cho nhieu don.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_payments_ref ON payments(method, ref) WHERE ref <> ''`,
		// docs: bai viet tai lieu (markdown) admin tu soan, hien o /docs.
		`CREATE TABLE IF NOT EXISTS docs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			slug TEXT UNIQUE NOT NULL,
			category TEXT DEFAULT '',
			title TEXT NOT NULL,
			body TEXT NOT NULL,
			sort_order INTEGER NOT NULL DEFAULT 0,
			is_published INTEGER NOT NULL DEFAULT 1,
			updated_at INTEGER NOT NULL
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	// Migration cot moi (idempotent) cho DB da ton tai tu Giai doan 1.
	db.addColumn("packages", "duration_days", "INTEGER NOT NULL DEFAULT 0")
	db.addColumn("packages", "duration_hours", "INTEGER NOT NULL DEFAULT 0")
	db.addColumn("users", "google_id", "TEXT DEFAULT ''")
	db.addColumn("users", "avatar_url", "TEXT DEFAULT ''")
	db.addColumn("users", "auth_provider", "TEXT DEFAULT 'password'")
	db.addColumn("users", "password_hash", "TEXT DEFAULT ''") // no-op neu da co
	db.addColumn("orders", "pay_method", "TEXT DEFAULT ''")
	// Don gia han: tro toi issued_keys.id cua key can gia han (0 = don mua moi).
	db.addColumn("orders", "renew_key_id", "INTEGER NOT NULL DEFAULT 0")
	db.addColumn("orders", "pay_amount", "REAL NOT NULL DEFAULT 0") // so tien le duy nhat (crypto) hoac so tien cong
	return nil
}

// seedPackages nap 5 goi mac dinh neu bang packages con trong.
func (db *DB) seedPackages() error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM packages`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	type pk struct {
		slug, name          string
		credits             int
		orig, price         float64
		featured            bool
	}
	seed := []pk{
		{"pro", "Pro", 1000, 20, 3, false},
		{"pro-plus", "Pro+", 2000, 40, 5, false},
		{"pro-max", "Pro Max", 5000, 100, 15, true},
		{"power", "Power", 10000, 200, 25, false},
		{"max", "MAX", 50000, 1000, 50, false},
	}
	for i, p := range seed {
		featured := 0
		if p.featured {
			featured = 1
		}
		_, err := db.Exec(
			`INSERT INTO packages(slug,name,credits,original_price_usd,price_usd,sort_order,is_active,is_featured)
			 VALUES(?,?,?,?,?,?,1,?)`,
			p.slug, p.name, p.credits, p.orig, p.price, i, featured,
		)
		if err != nil {
			return err
		}
	}
	log.Println("seeded 5 default packages")
	return nil
}

// getSetting doc 1 setting, tra ve def neu chua co.
func (db *DB) getSetting(key, def string) string {
	var v string
	err := db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if err != nil {
		return def
	}
	return v
}

// setSetting ghi/ghi de 1 setting.
func (db *DB) setSetting(key, value string) error {
	_, err := db.Exec(
		`INSERT INTO settings(key,value) VALUES(?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, value,
	)
	return err
}

// addColumn them cot vao bang neu chua ton tai (idempotent migration).
// SQLite khong ho tro "ADD COLUMN IF NOT EXISTS" -> bo qua loi "duplicate column".
func (db *DB) addColumn(table, column, def string) {
	_, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + def)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		// Cac loi khac (vd bang chua ton tai) chi log, khong fatal.
		log.Printf("addColumn %s.%s: %v", table, column, err)
	}
}

// cleanupSessions xoa session het han (goi dinh ky).
func (db *DB) cleanupSessions() {
	for {
		time.Sleep(1 * time.Hour)
		if _, err := db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().Unix()); err != nil {
			log.Printf("cleanup sessions error: %v", err)
		}
	}
}
