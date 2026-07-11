// oauth_google.go - dang nhap nhanh voi Google (OAuth 2.0).
// Luong: /auth/google -> redirect toi Google -> callback -> doi code lay token
// -> lay email da verify -> tao/lien ket user -> tao session.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var googleClient = &http.Client{Timeout: 15 * time.Second}

// googleAuthURL tao URL redirect toi Google kem state (chong CSRF).
func (app *App) googleRedirectURI() string {
	return app.baseURL + "/auth/google/callback"
}

// handleGoogleLogin sinh state, luu vao cookie, redirect toi Google.
func (app *App) handleGoogleLogin(w http.ResponseWriter, r *http.Request) {
	if !app.google.Enabled() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	state := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name: "g_state", Value: state, Path: "/", MaxAge: 600,
		HttpOnly: true, Secure: app.secureCookie, SameSite: http.SameSiteLaxMode,
	})
	q := url.Values{}
	q.Set("client_id", app.google.ClientID)
	q.Set("redirect_uri", app.googleRedirectURI())
	q.Set("response_type", "code")
	q.Set("scope", "openid email profile")
	q.Set("state", state)
	http.Redirect(w, r, "https://accounts.google.com/o/oauth2/v2/auth?"+q.Encode(), http.StatusSeeOther)
}

// handleGoogleCallback xac thuc state, doi code lay token, lay thong tin user.
func (app *App) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	if !app.google.Enabled() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	// Kiem tra state khop cookie.
	c, err := r.Cookie("g_state")
	if err != nil || c.Value == "" || c.Value != r.URL.Query().Get("state") {
		http.Error(w, "state khong hop le", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Doi code lay access token.
	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", app.google.ClientID)
	form.Set("client_secret", app.google.ClientSecret)
	form.Set("redirect_uri", app.googleRedirectURI())
	form.Set("grant_type", "authorization_code")
	resp, err := googleClient.PostForm("https://oauth2.googleapis.com/token", form)
	if err != nil {
		http.Error(w, "loi doi token", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	tdata, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(tdata, &tok); err != nil || tok.AccessToken == "" {
		http.Error(w, "token khong hop le", http.StatusBadGateway)
		return
	}

	// Lay thong tin user.
	ureq, _ := http.NewRequest(http.MethodGet, "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	ureq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	uresp, err := googleClient.Do(ureq)
	if err != nil {
		http.Error(w, "loi lay userinfo", http.StatusBadGateway)
		return
	}
	defer uresp.Body.Close()
	udata, _ := io.ReadAll(io.LimitReader(uresp.Body, 1<<20))
	var info struct {
		ID            string `json:"id"`
		Email         string `json:"email"`
		VerifiedEmail bool   `json:"verified_email"`
		Name          string `json:"name"`
		Picture       string `json:"picture"`
	}
	if err := json.Unmarshal(udata, &info); err != nil || info.Email == "" {
		http.Error(w, "khong lay duoc email", http.StatusBadGateway)
		return
	}

	userID, err := app.upsertGoogleUser(info.ID, strings.ToLower(info.Email), info.Name, info.Picture)
	if err != nil {
		http.Error(w, "loi tao tai khoan", http.StatusInternalServerError)
		return
	}
	_ = app.createSession(w, userID, false)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

// upsertGoogleUser tao user moi hoac lien ket google_id voi user email san co.
func (app *App) upsertGoogleUser(googleID, email, name, avatar string) (int64, error) {
	var id int64
	err := app.db.QueryRow(`SELECT id FROM users WHERE email=?`, email).Scan(&id)
	if err == nil {
		// da co user -> cap nhat google_id/avatar.
		_, _ = app.db.Exec(
			`UPDATE users SET google_id=?, avatar_url=?, auth_provider='google' WHERE id=?`,
			googleID, avatar, id,
		)
		return id, nil
	}
	// tao moi (password_hash rong vi dang nhap qua google).
	res, err := app.db.Exec(
		`INSERT INTO users(email,password_hash,full_name,created_at,google_id,avatar_url,auth_provider)
		 VALUES(?,?,?,?,?,?, 'google')`,
		email, "", name, time.Now().Unix(), googleID, avatar,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
