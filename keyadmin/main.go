// keyadmin - API co quyen (bearer token) cho bot Telegram tao & quan ly API key
// cua Kiro-Go. Khac voi keycheck (chi doc, cong khai, khong mat khau), keyadmin
// GIU ADMIN_PASSWORD va goi admin API cua kiro-go de tao key / bat-tat / doi han.
//
// Han dung duoc ma hoa trong TEN key (dong bo voi UI + cron):
//   " #exp=<unix>"    -> key con han, het han vao thoi diem unix (giay)
//   " #pause=<giay>"  -> dong ho tam dung, con lai bay nhieu giay
//   " #exp=YYYY-MM-DD"-> dinh dang cu (van doc duoc, het han cuoi ngay do)
//   (khong marker)    -> vinh vien
//
// Bao mat: bearer token so sanh constant-time, bind localhost (sau reverse proxy),
// khong log key, gioi han body, chi dung thu vien chuan.
package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	listenAddr = envOr("KEYADMIN_LISTEN", ":8082")
	kiroBase   = strings.TrimRight(envOr("KEYADMIN_KIRO_BASE", "http://kiro-go:8080"), "/")
	adminPass  = os.Getenv("ADMIN_PASSWORD")
	authToken  = os.Getenv("KEYADMIN_TOKEN")
)

var httpClient = &http.Client{Timeout: 15 * time.Second}

var (
	pauseRe   = regexp.MustCompile(`#pause=(\d+)`)
	expDateRe = regexp.MustCompile(`#exp=(\d{4}-\d{2}-\d{2})`)
	expUnixRe = regexp.MustCompile(`#exp=(\d+)`)
	stripRe   = regexp.MustCompile(`\s*#(?:exp=\d{4}-\d{2}-\d{2}|exp=\d+|pause=\d+)`)
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ================= Marker han dung =================

type expiryState struct {
	Mode        string `json:"mode"` // none | active | paused
	ExpiresAt   int64  `json:"expiresAt,omitempty"`
	SecondsLeft int64  `json:"secondsLeft"`
}

func parseExpiry(name string, now int64) expiryState {
	if m := pauseRe.FindStringSubmatch(name); m != nil {
		v, _ := strconv.ParseInt(m[1], 10, 64)
		return expiryState{Mode: "paused", SecondsLeft: v}
	}
	if m := expDateRe.FindStringSubmatch(name); m != nil {
		if t, err := time.ParseInLocation("2006-01-02", m[1], time.Local); err == nil {
			exp := t.Unix() + 86399 // cuoi ngay 23:59:59 gio local
			return expiryState{Mode: "active", ExpiresAt: exp, SecondsLeft: exp - now}
		}
	}
	if m := expUnixRe.FindStringSubmatch(name); m != nil {
		v, _ := strconv.ParseInt(m[1], 10, 64)
		return expiryState{Mode: "active", ExpiresAt: v, SecondsLeft: v - now}
	}
	return expiryState{Mode: "none"}
}

func stripMarkers(name string) string {
	return strings.TrimSpace(stripRe.ReplaceAllString(name, ""))
}

func withExpUnix(name string, exp int64) string {
	return strings.TrimSpace(stripMarkers(name) + " #exp=" + strconv.FormatInt(exp, 10))
}

func withPause(name string, seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	return strings.TrimSpace(stripMarkers(name) + " #pause=" + strconv.FormatInt(seconds, 10))
}

// ================= Goi admin API cua kiro-go =================

type apiKey struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Enabled     bool    `json:"enabled"`
	TokenLimit  int64   `json:"tokenLimit"`
	CreditLimit float64 `json:"creditLimit"`
	TokensUsed  int64   `json:"tokensUsed"`
	CreditsUsed float64 `json:"creditsUsed"`
}

func kiroReq(method, path string, body interface{}) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, kiroBase+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-Admin-Password", adminPass)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return data, resp.StatusCode, err
}

func kiroList() ([]apiKey, error) {
	data, status, err := kiroReq("GET", "/admin/api/api-keys", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, &httpErr{status, "kiro list failed"}
	}
	var wrap struct {
		ApiKeys []apiKey `json:"apiKeys"`
	}
	if err := json.Unmarshal(data, &wrap); err == nil && wrap.ApiKeys != nil {
		return wrap.ApiKeys, nil
	}
	var arr []apiKey
	if err := json.Unmarshal(data, &arr); err != nil {
		return nil, err
	}
	return arr, nil
}

func kiroFind(id string) (apiKey, error) {
	keys, err := kiroList()
	if err != nil {
		return apiKey{}, err
	}
	for _, k := range keys {
		if k.ID == id {
			return k, nil
		}
	}
	return apiKey{}, &httpErr{http.StatusNotFound, "key not found"}
}

func kiroUpdate(id string, patch map[string]interface{}) error {
	data, status, err := kiroReq("PUT", "/admin/api/api-keys/"+id, patch)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return &httpErr{status, "kiro update failed: " + string(data)}
	}
	return nil
}

func kiroCreate(name string, tokenLimit int64, creditLimit float64, enabled bool) (string, error) {
	payload := map[string]interface{}{
		"name": name, "enabled": enabled,
		"tokenLimit": tokenLimit, "creditLimit": creditLimit,
	}
	data, status, err := kiroReq("POST", "/admin/api/api-keys", payload)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", &httpErr{status, "kiro create failed: " + string(data)}
	}
	var d struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return "", err
	}
	return d.Key, nil
}

type httpErr struct {
	code int
	msg  string
}

func (e *httpErr) Error() string { return e.msg }

// ================= HTTP handlers =================

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusBadGateway
	if he, ok := err.(*httpErr); ok {
		code = he.code
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// auth kiem tra bearer token constant-time.
func auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		tok = strings.TrimSpace(tok)
		if authToken == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(authToken)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func decodeBody(r *http.Request, v interface{}) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 8192)
	return json.NewDecoder(r.Body).Decode(v)
}

// enrich them trang thai han dung vao thong tin key tra ve.
type keyView struct {
	apiKey
	Expiry expiryState `json:"expiry"`
	Name   string      `json:"name"` // ten da bo marker cho de doc
}

func viewOf(k apiKey, now int64) keyView {
	return keyView{apiKey: k, Expiry: parseExpiry(k.Name, now), Name: stripMarkers(k.Name)}
}

// matchFilter kiem tra 1 key co thoa filter khong.
//   enabled: "true"|"false"|""  (loc theo bat/tat)
//   expiry:  "active"|"paused"|"expired"|"permanent"|""  (loc theo trang thai han)
//   q:       chuoi tim trong ten (da bo marker), khong phan biet hoa thuong
func matchFilter(v keyView, enabled, expiry, q string) bool {
	if enabled == "true" && !v.Enabled {
		return false
	}
	if enabled == "false" && v.Enabled {
		return false
	}
	if expiry != "" {
		mode := v.Expiry.Mode
		isExpired := mode == "active" && v.Expiry.SecondsLeft <= 0
		switch expiry {
		case "expired":
			if !isExpired {
				return false
			}
		case "active":
			if !(mode == "active" && !isExpired) {
				return false
			}
		case "paused":
			if mode != "paused" {
				return false
			}
		case "permanent":
			if mode != "none" {
				return false
			}
		}
	}
	if q != "" && !strings.Contains(strings.ToLower(v.Name), strings.ToLower(q)) {
		return false
	}
	return true
}

func handleList(w http.ResponseWriter, r *http.Request) {
	keys, err := kiroList()
	if err != nil {
		writeErr(w, err)
		return
	}
	// Filter qua query params (khong bat buoc): ?enabled=&expiry=&q=
	fEnabled := strings.TrimSpace(r.URL.Query().Get("enabled"))
	fExpiry := strings.TrimSpace(r.URL.Query().Get("expiry"))
	fQ := strings.TrimSpace(r.URL.Query().Get("q"))

	now := time.Now().Unix()
	out := make([]keyView, 0, len(keys))
	for _, k := range keys {
		v := viewOf(k, now)
		if matchFilter(v, fEnabled, fExpiry, fQ) {
			out = append(out, v)
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"keys":  out,
		"count": len(out),
		"total": len(keys),
	})
}

func handleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string  `json:"name"`
		Count       int     `json:"count"`
		TokenLimit  int64   `json:"tokenLimit"`
		CreditLimit float64 `json:"creditLimit"`
		Days        int     `json:"days"`
		Hours       int     `json:"hours"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if req.Count <= 0 {
		req.Count = 1
	}
	if req.Count > 500 {
		req.Count = 500
	}

	var exp int64
	if req.Days > 0 || req.Hours > 0 {
		exp = time.Now().Unix() + int64(req.Days)*86400 + int64(req.Hours)*3600
	}

	type created struct {
		Name string `json:"name"`
		Key  string `json:"key"`
	}
	var out []created
	errCount := 0
	base := strings.TrimSpace(req.Name)
	for i := 1; i <= req.Count; i++ {
		name := base
		if req.Count > 1 {
			if name == "" {
				name = "key"
			}
			name = name + "-" + strconv.Itoa(i)
		}
		if exp > 0 {
			name = withExpUnix(name, exp)
		}
		key, err := kiroCreate(name, req.TokenLimit, req.CreditLimit, true)
		if err != nil {
			errCount++
			continue
		}
		out = append(out, created{Name: name, Key: key})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"created": out, "errors": errCount})
}

func handlePause(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeBody(r, &req); err != nil || req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	k, err := kiroFind(req.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	now := time.Now().Unix()
	st := parseExpiry(k.Name, now)
	switch st.Mode {
	case "paused":
		writeJSON(w, http.StatusOK, viewOf(k, now)) // da tam dung
		return
	case "none":
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key khong co han de tam dung"})
		return
	}
	left := st.SecondsLeft
	if left < 0 {
		left = 0
	}
	newName := withPause(k.Name, left)
	if err := kiroUpdate(req.ID, map[string]interface{}{"name": newName}); err != nil {
		writeErr(w, err)
		return
	}
	log.Printf("pause id=%s left=%ds", req.ID, left)
	k.Name = newName
	writeJSON(w, http.StatusOK, viewOf(k, now))
}

func handleResume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeBody(r, &req); err != nil || req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	k, err := kiroFind(req.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	now := time.Now().Unix()
	st := parseExpiry(k.Name, now)
	if st.Mode != "paused" {
		writeJSON(w, http.StatusOK, viewOf(k, now)) // khong o trang thai tam dung
		return
	}
	exp := now + st.SecondsLeft
	newName := withExpUnix(k.Name, exp)
	if err := kiroUpdate(req.ID, map[string]interface{}{"name": newName}); err != nil {
		writeErr(w, err)
		return
	}
	log.Printf("resume id=%s exp=%d", req.ID, exp)
	k.Name = newName
	writeJSON(w, http.StatusOK, viewOf(k, now))
}

func handleAddHours(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string `json:"id"`
		Hours int    `json:"hours"`
	}
	if err := decodeBody(r, &req); err != nil || req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	k, err := kiroFind(req.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	now := time.Now().Unix()
	st := parseExpiry(k.Name, now)
	add := int64(req.Hours) * 3600
	var newName string
	switch st.Mode {
	case "paused":
		left := st.SecondsLeft + add
		if left < 0 {
			left = 0
		}
		newName = withPause(k.Name, left)
	case "active":
		exp := st.ExpiresAt + add
		if exp < now {
			exp = now
		}
		newName = withExpUnix(k.Name, exp)
	default: // none: bat dau dem tu bay gio
		exp := now + add
		if exp < now {
			exp = now
		}
		newName = withExpUnix(k.Name, exp)
	}
	if err := kiroUpdate(req.ID, map[string]interface{}{"name": newName}); err != nil {
		writeErr(w, err)
		return
	}
	log.Printf("add-hours id=%s hours=%d", req.ID, req.Hours)
	k.Name = newName
	writeJSON(w, http.StatusOK, viewOf(k, now))
}

func handleEnable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := decodeBody(r, &req); err != nil || req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	if err := kiroUpdate(req.ID, map[string]interface{}{"enabled": req.Enabled}); err != nil {
		writeErr(w, err)
		return
	}
	log.Printf("enable id=%s enabled=%v", req.ID, req.Enabled)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleStats: bo dem so key theo trang thai (cho bot hien thi tong quan).
func handleStats(w http.ResponseWriter, r *http.Request) {
	keys, err := kiroList()
	if err != nil {
		writeErr(w, err)
		return
	}
	now := time.Now().Unix()
	var total, enabled, disabled, active, paused, expired, permanent int
	for _, k := range keys {
		total++
		if k.Enabled {
			enabled++
		} else {
			disabled++
		}
		st := parseExpiry(k.Name, now)
		switch st.Mode {
		case "active":
			if st.SecondsLeft <= 0 {
				expired++
			} else {
				active++
			}
		case "paused":
			paused++
		default:
			permanent++
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total":     total,
		"enabled":   enabled,
		"disabled":  disabled,
		"active":    active,    // con han, dang chay
		"paused":    paused,    // tam dung dong ho
		"expired":   expired,   // qua han (cho cron tat)
		"permanent": permanent, // khong co han
	})
}

// handleSetExpiry: DAT thoi han tuyet doi = now + days*86400 + hours*3600.
// days=0 && hours=0 -> xoa han (vinh vien).
func handleSetExpiry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string `json:"id"`
		Days  int    `json:"days"`
		Hours int    `json:"hours"`
	}
	if err := decodeBody(r, &req); err != nil || req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	k, err := kiroFind(req.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	now := time.Now().Unix()
	var newName string
	if req.Days == 0 && req.Hours == 0 {
		newName = stripMarkers(k.Name) // vinh vien
	} else {
		exp := now + int64(req.Days)*86400 + int64(req.Hours)*3600
		if exp < now {
			exp = now
		}
		newName = withExpUnix(k.Name, exp)
	}
	if err := kiroUpdate(req.ID, map[string]interface{}{"name": newName}); err != nil {
		writeErr(w, err)
		return
	}
	log.Printf("set-expiry id=%s days=%d hours=%d", req.ID, req.Days, req.Hours)
	k.Name = newName
	writeJSON(w, http.StatusOK, viewOf(k, now))
}

// handleRename: doi ten key, GIU NGUYEN marker han (#exp/#pause) hien co.
func handleRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := decodeBody(r, &req); err != nil || req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	k, err := kiroFind(req.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	now := time.Now().Unix()
	st := parseExpiry(k.Name, now)
	// Ten moi: bo marker (neu bot lo gui kem) roi ghep lai marker han cu.
	base := stripMarkers(req.Name)
	var newName string
	switch st.Mode {
	case "paused":
		newName = withPause(base, st.SecondsLeft)
	case "active":
		newName = withExpUnix(base, st.ExpiresAt)
	default:
		newName = base
	}
	if err := kiroUpdate(req.ID, map[string]interface{}{"name": newName}); err != nil {
		writeErr(w, err)
		return
	}
	log.Printf("rename id=%s", req.ID)
	k.Name = newName
	writeJSON(w, http.StatusOK, viewOf(k, now))
}

// handleSetCredit: dat creditLimit tuyet doi cho key.
func handleSetCredit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     string  `json:"id"`
		Credit float64 `json:"credit"`
	}
	if err := decodeBody(r, &req); err != nil || req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	if req.Credit < 0 {
		req.Credit = 0
	}
	if err := kiroUpdate(req.ID, map[string]interface{}{"creditLimit": req.Credit}); err != nil {
		writeErr(w, err)
		return
	}
	log.Printf("set-credit id=%s credit=%v", req.ID, req.Credit)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAddCredit: cong/tru creditLimit (delta am de tru). Khong cho xuong duoi creditsUsed.
func handleAddCredit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string  `json:"id"`
		Delta float64 `json:"delta"`
	}
	if err := decodeBody(r, &req); err != nil || req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	k, err := kiroFind(req.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	newLimit := k.CreditLimit + req.Delta
	if newLimit < k.CreditsUsed {
		newLimit = k.CreditsUsed // khong cho thap hon so da dung
	}
	if newLimit < 0 {
		newLimit = 0
	}
	if err := kiroUpdate(req.ID, map[string]interface{}{"creditLimit": newLimit}); err != nil {
		writeErr(w, err)
		return
	}
	log.Printf("add-credit id=%s delta=%v new=%v", req.ID, req.Delta, newLimit)
	now := time.Now().Unix()
	k.CreditLimit = newLimit
	writeJSON(w, http.StatusOK, viewOf(k, now))
}

// handleDelete: xoa key trong kiro-go. Neu kiro-go khong ho tro DELETE -> fallback disable.
func handleDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeBody(r, &req); err != nil || req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	data, status, err := kiroReq("DELETE", "/admin/api/api-keys/"+req.ID, nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	if status >= 200 && status < 300 {
		log.Printf("delete id=%s ok", req.ID)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "deleted": true})
		return
	}
	// Fallback: kiro-go khong ho tro DELETE -> vo hieu hoa key.
	if err := kiroUpdate(req.ID, map[string]interface{}{"enabled": false}); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "delete+disable failed: " + string(data)})
		return
	}
	log.Printf("delete id=%s fallback-disabled", req.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "deleted": false})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func main() {
	if authToken == "" {
		log.Fatal("KEYADMIN_TOKEN chua duoc dat - tu choi khoi dong de tranh API khong bao ve")
	}
	if adminPass == "" {
		log.Fatal("ADMIN_PASSWORD chua duoc dat")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealth)
	mux.HandleFunc("/api/keys", auth(handleList))
	mux.HandleFunc("/api/keys/stats", auth(handleStats))
	mux.HandleFunc("/api/keys/rename", auth(handleRename))
	mux.HandleFunc("/api/keys/create", auth(handleCreate))
	mux.HandleFunc("/api/keys/pause", auth(handlePause))
	mux.HandleFunc("/api/keys/resume", auth(handleResume))
	mux.HandleFunc("/api/keys/add-hours", auth(handleAddHours))
	mux.HandleFunc("/api/keys/enable", auth(handleEnable))
	mux.HandleFunc("/api/keys/set-credit", auth(handleSetCredit))
	mux.HandleFunc("/api/keys/add-credit", auth(handleAddCredit))
	mux.HandleFunc("/api/keys/set-expiry", auth(handleSetExpiry))
	mux.HandleFunc("/api/keys/delete", auth(handleDelete))

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 14,
	}
	log.Printf("keyadmin listening on %s (kiro=%s)", listenAddr, kiroBase)
	log.Fatal(srv.ListenAndServe())
}
