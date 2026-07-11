// auth.go - dang ky/dang nhap khach (bcrypt) + session cookie + auth admin.
package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "sf_session"
	sessionTTL    = 30 * 24 * time.Hour
)

// User dai dien 1 khach hang.
type User struct {
	ID        int64
	Email     string
	FullName  string
	CreatedAt int64
	IsBlocked bool
}

// randToken sinh token ngau nhien hex (dung cho session).
func randToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// hashPassword ma hoa mat khau bang bcrypt.
func hashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(h), err
}

// checkPassword so khop mat khau voi hash.
func checkPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// registerUser tao khach moi. Tra ve loi neu email da ton tai.
func (app *App) registerUser(email, pw, name string) (int64, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	hash, err := hashPassword(pw)
	if err != nil {
		return 0, err
	}
	res, err := app.db.Exec(
		`INSERT INTO users(email,password_hash,full_name,created_at) VALUES(?,?,?,?)`,
		email, hash, strings.TrimSpace(name), time.Now().Unix(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// authenticate kiem tra email + mat khau. Tra ve user neu dung.
func (app *App) authenticate(email, pw string) (*User, bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	var (
		u    User
		hash string
	)
	err := app.db.QueryRow(
		`SELECT id,email,password_hash,full_name,created_at,is_blocked FROM users WHERE email=?`,
		email,
	).Scan(&u.ID, &u.Email, &hash, &u.FullName, &u.CreatedAt, &u.IsBlocked)
	if err != nil {
		// van chay bcrypt gia de chuan hoa thoi gian phan hoi (chong user enumeration)
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$invalidinvalidinvalidinvalidinvalidinvalidinvalidinv"), []byte(pw))
		return nil, false
	}
	if u.IsBlocked || !checkPassword(hash, pw) {
		return nil, false
	}
	return &u, true
}

// createSession tao session moi va set cookie.
func (app *App) createSession(w http.ResponseWriter, userID int64, isAdmin bool) error {
	tok := randToken()
	now := time.Now()
	exp := now.Add(sessionTTL)
	adminInt := 0
	if isAdmin {
		adminInt = 1
	}
	_, err := app.db.Exec(
		`INSERT INTO sessions(token,user_id,is_admin,created_at,expires_at) VALUES(?,?,?,?,?)`,
		tok, userID, adminInt, now.Unix(), exp.Unix(),
	)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    tok,
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		Secure:   app.secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// session mo ta phien dang nhap hien tai.
type session struct {
	UserID  int64
	IsAdmin bool
	Email   string
}

// currentSession doc session tu cookie. Tra ve nil neu chua dang nhap.
func (app *App) currentSession(r *http.Request) *session {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	var (
		s       session
		adminI  int
		expires int64
	)
	err = app.db.QueryRow(
		`SELECT s.user_id, s.is_admin, s.expires_at, u.email
		 FROM sessions s JOIN users u ON u.id=s.user_id
		 WHERE s.token=?`, c.Value,
	).Scan(&s.UserID, &adminI, &expires, &s.Email)
	if err != nil || expires < time.Now().Unix() {
		return nil
	}
	s.IsAdmin = adminI == 1
	return &s
}

// destroySession xoa session hien tai + clear cookie.
func (app *App) destroySession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_, _ = app.db.Exec(`DELETE FROM sessions WHERE token=?`, c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
	})
}

// adminLogin so khop mat khau admin (constant-time) voi ADMIN_PASSWORD.
func (app *App) adminLogin(pw string) bool {
	if app.adminPassword == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(pw), []byte(app.adminPassword)) == 1
}

// requireUser middleware: bat buoc dang nhap khach.
func (app *App) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := app.currentSession(r)
		if s == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// requireAdmin middleware: bat buoc phien admin.
func (app *App) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := app.currentSession(r)
		if s == nil || !s.IsAdmin {
			http.Redirect(w, r, "/frontadmin/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}
