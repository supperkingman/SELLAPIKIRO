// keycheck - sidecar cho phep khach kiem tra API key cua Kiro-Go ma KHONG can
// mat khau admin va KHONG ton token / khong cham pool.
//
// Cach hoat dong: doc truc tiep config.json (mount read-only) - cung nguon du
// lieu kiro-go dung - de xac thuc key (so khop constant-time) va tra ve usage
// (token/credit da dung & con lai). Vi khong goi /v1 nen endpoint cong khai nay
// khong the bi loi dung de rut token hay DoS upstream.
//
// Bao mat: rate limit theo IP, ban tam thoi sau nhieu lan sai, validate dinh
// dang, so sanh constant-time, chuan hoa thoi gian phan hoi (chong timing
// attack), khong log key, gioi han body, security headers.
package main

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- Cau hinh qua bien moi truong (co mac dinh hop ly) ----
var (
	configPath  = envOr("KEYCHECK_CONFIG_PATH", "/data/config.json")
	htmlPath    = envOr("KEYCHECK_HTML_PATH", "/app/check-key.html")
	listenAddr  = envOr("KEYCHECK_LISTEN", ":8081")
	minRespTime = 150 * time.Millisecond // chuan hoa thoi gian phan hoi toi thieu

	// Cloudflare Turnstile: neu dat secret -> BAT BUOC captcha truoc khi check key.
	// De trong -> bo qua captcha (tuong thich nguoc, khong pha vo neu chua cau hinh).
	turnstileSecret = os.Getenv("TURNSTILE_SECRET")

	// Rate limit: token bucket moi IP
	rlCapacity  = 10              // toi da 10 lan
	rlRefillEv  = 6 * time.Second // hoi 1 token moi 6s (~10/phut)
	// Ban tam thoi
	banThreshold = 8                // 8 lan key sai lien tuc
	banDuration  = 15 * time.Minute // -> khoa 15 phut
)

// ---- Cau truc config.json cua Kiro-Go (chi cac truong can dung) ----
type apiKeyEntry struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Key           string  `json:"key"`
	Enabled       bool    `json:"enabled"`
	CreatedAt     int64   `json:"createdAt"`
	LastUsedAt    int64   `json:"lastUsedAt"`
	TokenLimit    int64   `json:"tokenLimit"`
	CreditLimit   float64 `json:"creditLimit"`
	TokensUsed    int64   `json:"tokensUsed"`
	CreditsUsed   float64 `json:"creditsUsed"`
	RequestsCount int64   `json:"requestsCount"`

	// keyBytes = []byte(Key) tinh san luc load de tranh cap phat moi request
	// trong vong lap so khop constant-time. Truong khong export -> json bo qua.
	keyBytes []byte
}

type kiroConfig struct {
	RequireApiKey bool          `json:"requireApiKey"`
	ApiKeys       []apiKeyEntry `json:"apiKeys"`
}

// ---- Phan hoi tra ve cho khach ----
type checkResponse struct {
	Valid         bool     `json:"valid"`
	Enabled       bool     `json:"enabled"`
	Message       string   `json:"message"`
	Name          string   `json:"name,omitempty"`
	TokenLimited  bool     `json:"tokenLimited"`
	TokenLimit    int64    `json:"tokenLimit"`
	TokensUsed    int64    `json:"tokensUsed"`
	TokensLeft    int64    `json:"tokensLeft"`
	CreditLimited bool     `json:"creditLimited"`
	CreditLimit   float64  `json:"creditLimit"`
	CreditsUsed   float64  `json:"creditsUsed"`
	CreditsLeft   float64  `json:"creditsLeft"`
	RequestsCount int64    `json:"requestsCount"`
	// Han su dung (giai ma tu marker #exp trong ten key)
	ExpiryMode  string `json:"expiryMode"`            // "none" | "active" | "paused"
	ExpiresAt   int64  `json:"expiresAt,omitempty"`   // unix giay, khi active
	SecondsLeft int64  `json:"secondsLeft,omitempty"` // con lai bao nhieu giay
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ---- Giai ma han su dung tu ten key (khop keyadmin/main.go) ----
//   #exp=<unix>      -> con han, het han vao thoi diem unix (giay)
//   #exp=YYYY-MM-DD  -> dinh dang cu (het han cuoi ngay do, gio local ~UTC)
//   #pause=<giay>    -> dong ho tam dung, con lai bay nhieu giay
var (
	kcPauseRe   = regexp.MustCompile(`#pause=(\d+)`)
	kcExpDateRe = regexp.MustCompile(`#exp=(\d{4})-(\d{2})-(\d{2})`)
	kcExpUnixRe = regexp.MustCompile(`#exp=(\d+)`)
)

// parseKeyExpiry tra ve (mode, expiresAt, secondsLeft). now la unix giay.
func parseKeyExpiry(name string, now int64) (string, int64, int64) {
	if m := kcPauseRe.FindStringSubmatch(name); m != nil {
		secs, _ := strconv.ParseInt(m[1], 10, 64)
		return "paused", 0, secs
	}
	// exp date phai thu TRUOC exp unix (vi \d+ se bat "2026" trong 2026-01-01).
	if m := kcExpDateRe.FindStringSubmatch(name); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		exp := time.Date(y, time.Month(mo), d, 23, 59, 59, 0, time.UTC).Unix()
		return "active", exp, exp - now
	}
	if m := kcExpUnixRe.FindStringSubmatch(name); m != nil {
		exp, _ := strconv.ParseInt(m[1], 10, 64)
		return "active", exp, exp - now
	}
	return "none", 0, 0
}

// stripExpiryMarkers bo cac marker #exp/#pause khoi ten hien thi.
var kcStripRe = regexp.MustCompile(`\s*#(?:exp=\d{4}-\d{2}-\d{2}|exp=\d+|pause=\d+)`)

func stripExpiryMarkers(name string) string {
	return strings.TrimSpace(kcStripRe.ReplaceAllString(name, ""))
}

// ================= Rate limiting + ban =================

type bucket struct {
	tokens     float64
	lastRefill time.Time
	failStreak int
	bannedTill time.Time
}

type limiter struct {
	mu sync.Mutex
	m  map[string]*bucket
}

func newLimiter() *limiter {
	l := &limiter{m: make(map[string]*bucket)}
	// don dep dinh ky cac IP cu de tranh phinh bo nho
	go func() {
		for {
			time.Sleep(10 * time.Minute)
			l.mu.Lock()
			now := time.Now()
			for ip, b := range l.m {
				if now.Sub(b.lastRefill) > 30*time.Minute && now.After(b.bannedTill) {
					delete(l.m, ip)
				}
			}
			l.mu.Unlock()
		}
	}()
	return l
}

func (l *limiter) get(ip string) *bucket {
	b := l.m[ip]
	if b == nil {
		b = &bucket{tokens: float64(rlCapacity), lastRefill: time.Now()}
		l.m[ip] = b
	}
	return b
}

// allow tra ve (duoc phep, dang bi ban)
func (l *limiter) allow(ip string) (bool, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.get(ip)
	now := time.Now()
	if now.Before(b.bannedTill) {
		return false, true
	}
	// hoi token
	elapsed := now.Sub(b.lastRefill)
	refill := float64(elapsed / rlRefillEv)
	if refill > 0 {
		b.tokens += refill
		if b.tokens > float64(rlCapacity) {
			b.tokens = float64(rlCapacity)
		}
		b.lastRefill = now
	}
	if b.tokens < 1 {
		return false, false
	}
	b.tokens--
	return true, false
}

// recordResult cap nhat chuoi that bai va ban neu vuot nguong
func (l *limiter) recordResult(ip string, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.get(ip)
	if ok {
		b.failStreak = 0
		return
	}
	b.failStreak++
	if b.failStreak >= banThreshold {
		b.bannedTill = time.Now().Add(banDuration)
		b.failStreak = 0
	}
}

// ================= Doc config (cache + reload theo mtime) =================

type configStore struct {
	mu      sync.RWMutex
	cfg     kiroConfig
	modTime time.Time
	loaded  bool
}

// ready cho biet config da nap thanh cong it nhat mot lan (chi doc cache,
// khong cham dia -> khong nam trong hot path cua request).
func (s *configStore) ready() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loaded
}

// watch reload config dinh ky trong goroutine nen. Chuyen os.Stat/ReadFile ra
// khoi hot path: request chi doc cache. load() da bo qua khi mtime khong doi.
func (s *configStore) watch(every time.Duration) {
	for {
		time.Sleep(every)
		if err := s.load(); err != nil {
			log.Printf("config reload error: %v", err)
		}
	}
}

func (s *configStore) load() error {
	fi, err := os.Stat(configPath)
	if err != nil {
		return err
	}
	s.mu.RLock()
	cached := fi.ModTime().Equal(s.modTime)
	s.mu.RUnlock()
	if cached {
		return nil
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var c kiroConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return err
	}
	for i := range c.ApiKeys {
		c.ApiKeys[i].keyBytes = []byte(c.ApiKeys[i].Key)
	}
	s.mu.Lock()
	s.cfg = c
	s.modTime = fi.ModTime()
	s.loaded = true
	s.mu.Unlock()
	return nil
}

// findKey so khop key bang constant-time tren TOAN BO danh sach (khong dung
// break som) de thoi gian khong phu thuoc vi tri key -> chong timing attack.
func (s *configStore) findKey(provided string) (apiKeyEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pb := []byte(provided)
	var match apiKeyEntry
	found := false
	for _, e := range s.cfg.ApiKeys {
		if subtle.ConstantTimeCompare(e.keyBytes, pb) == 1 {
			match = e
			found = true
		}
	}
	return match, found
}

// ================= Validate dinh dang key =================

// Kiro-Go sinh key dang "sk-" + 64 ky tu hex. Loai som cac chuoi sai dinh dang.
func validKeyFormat(k string) bool {
	if len(k) != 67 || !strings.HasPrefix(k, "sk-") {
		return false
	}
	_, err := hex.DecodeString(k[3:])
	return err == nil
}

// ================= HTTP handlers =================

var (
	store = &configStore{}
	lim   = newLimiter()
)

func clientIP(r *http.Request) string {
	// Cloudflare (proxy cam) ghi IP THAT cua khach vao CF-Connecting-IP. Uu tien
	// header nay khi site chay sau Cloudflare -> rate-limit/ban dung IP khach,
	// khong bi gop chung vao IP cua Cloudflare. Header do CF dat, client khong
	// gia mao duoc mien la ta chi cho dai IP Cloudflare ket noi toi VPS (xem
	// huong dan chong bypass trong security/README-DDOS.md).
	if cf := r.Header.Get("CF-Connecting-IP"); cf != "" {
		return strings.TrimSpace(cf)
	}
	// Proxy (OLS/Nginx) them IP that vao CUOI chuoi X-Forwarded-For. Client co the
	// tu gui XFF gia o dau chuoi, nhung IP CUOI luon la IP that do proxy ghi -> lay
	// phan tu cuoi cung de chong gia mao.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		last := strings.TrimSpace(parts[len(parts)-1])
		if last != "" {
			return last
		}
	}
	// Du phong: mot so proxy dung X-Real-IP.
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return strings.TrimSpace(ip)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func securityHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// normalizeTime dam bao moi request mat it nhat minRespTime (cong jitter nho)
// de thoi gian phan hoi key dung/sai khong phan biet duoc.
func normalizeTime(start time.Time) {
	elapsed := time.Since(start)
	jitter := time.Duration(time.Now().UnixNano()%40) * time.Millisecond
	target := minRespTime + jitter
	if elapsed < target {
		time.Sleep(target - elapsed)
	}
}

const genericInvalid = "API key không hợp lệ, đã bị vô hiệu hóa, hoặc không tồn tại."

// turnstileClient co timeout rieng de khong treo request neu Cloudflare cham.
var turnstileClient = &http.Client{Timeout: 8 * time.Second}

// verifyTurnstile goi Cloudflare siteverify. Tra ve true neu token hop le.
// remoteip giup Cloudflare cham diem chinh xac hon (khong bat buoc).
func verifyTurnstile(token, remoteip string) bool {
	if token == "" {
		return false
	}
	form := url.Values{}
	form.Set("secret", turnstileSecret)
	form.Set("response", token)
	if remoteip != "" {
		form.Set("remoteip", remoteip)
	}
	resp, err := turnstileClient.PostForm(
		"https://challenges.cloudflare.com/turnstile/v0/siteverify", form)
	if err != nil {
		log.Printf("turnstile verify error: %v", err)
		return false
	}
	defer resp.Body.Close()
	var out struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false
	}
	return out.Success
}

func handleCheck(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	securityHeaders(w)

	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	ip := clientIP(r)
	allowed, banned := lim.allow(ip)
	if !allowed {
		normalizeTime(start)
		if banned {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{
				"error": "Bạn đã thử quá nhiều lần. Vui lòng quay lại sau ít phút.",
			})
		} else {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{
				"error": "Quá nhiều yêu cầu. Vui lòng chờ một lát rồi thử lại.",
			})
		}
		return
	}

	// Gioi han body 4KB
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req struct {
		Key            string `json:"key"`
		TurnstileToken string `json:"turnstileToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		lim.recordResult(ip, false)
		normalizeTime(start)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Yêu cầu không hợp lệ."})
		return
	}

	// Captcha: neu da cau hinh TURNSTILE_SECRET -> bat buoc token hop le.
	// Chan bot tu dong do key hang loat (lop them, tren rate-limit + fail2ban).
	if turnstileSecret != "" {
		if !verifyTurnstile(req.TurnstileToken, ip) {
			lim.recordResult(ip, false)
			normalizeTime(start)
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "Xác minh captcha thất bại. Vui lòng tải lại trang và thử lại.",
			})
			return
		}
	}

	key := strings.TrimSpace(req.Key)

	// Sai dinh dang -> coi nhu that bai (van tinh vao ban) va tra thong diep chung
	if !validKeyFormat(key) {
		lim.recordResult(ip, false)
		normalizeTime(start)
		writeJSON(w, http.StatusOK, checkResponse{Valid: false, Message: genericInvalid})
		return
	}

	if !store.ready() {
		normalizeTime(start)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "Hệ thống tạm thời chưa sẵn sàng. Vui lòng thử lại sau.",
		})
		return
	}

	entry, found := store.findKey(key)
	if !found || !entry.Enabled {
		lim.recordResult(ip, false)
		log.Printf("check ip=%s result=invalid", ip)
		normalizeTime(start)
		writeJSON(w, http.StatusOK, checkResponse{Valid: false, Enabled: entry.Enabled, Message: genericInvalid})
		return
	}

	// Key hop le
	lim.recordResult(ip, true)
	log.Printf("check ip=%s result=valid key=%s", ip, maskKey(key))

	expMode, expAt, expLeft := parseKeyExpiry(entry.Name, time.Now().Unix())
	resp := checkResponse{
		Valid:         true,
		Enabled:       true,
		Name:          stripExpiryMarkers(entry.Name),
		Message:       "API key hợp lệ và đang hoạt động.",
		RequestsCount: entry.RequestsCount,
		ExpiryMode:    expMode,
		ExpiresAt:     expAt,
		SecondsLeft:   expLeft,
	}
	if entry.TokenLimit > 0 {
		resp.TokenLimited = true
		resp.TokenLimit = entry.TokenLimit
		resp.TokensUsed = entry.TokensUsed
		resp.TokensLeft = entry.TokenLimit - entry.TokensUsed
		if resp.TokensLeft < 0 {
			resp.TokensLeft = 0
		}
	} else {
		resp.TokensUsed = entry.TokensUsed
	}
	if entry.CreditLimit > 0 {
		resp.CreditLimited = true
		resp.CreditLimit = entry.CreditLimit
		resp.CreditsUsed = entry.CreditsUsed
		resp.CreditsLeft = entry.CreditLimit - entry.CreditsUsed
		if resp.CreditsLeft < 0 {
			resp.CreditsLeft = 0
		}
	} else {
		resp.CreditsUsed = entry.CreditsUsed
	}

	normalizeTime(start)
	writeJSON(w, http.StatusOK, resp)
}

func maskKey(k string) string {
	if len(k) < 11 {
		return "sk-***"
	}
	return k[:6] + "***" + k[len(k)-4:]
}

func handlePage(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w)
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeFile(w, r, htmlPath)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func main() {
	if err := store.load(); err != nil {
		log.Printf("WARNING: khong doc duoc config luc khoi dong (%v) - se thu lai o goroutine nen", err)
	}
	// Reload config trong nen thay vi trong hot path moi request.
	go store.watch(5 * time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/check-key", handleCheck)
	mux.HandleFunc("/check-key", handlePage)
	mux.HandleFunc("/healthz", handleHealth)

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 14, // 16KB
	}
	log.Printf("keycheck listening on %s (config=%s)", listenAddr, configPath)
	log.Fatal(srv.ListenAndServe())
}
