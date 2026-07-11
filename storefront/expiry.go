// expiry.go - giai ma han su dung tu ten key (marker #exp / #pause).
// Khop dinh dang cua keyadmin/main.go va keycheck.
package main

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	expPauseRe   = regexp.MustCompile(`#pause=(\d+)`)
	expDateReSF  = regexp.MustCompile(`#exp=(\d{4})-(\d{2})-(\d{2})`)
	expUnixReSF  = regexp.MustCompile(`#exp=(\d+)`)
	expStripReSF = regexp.MustCompile(`\s*#(?:exp=\d{4}-\d{2}-\d{2}|exp=\d+|pause=\d+)`)
)

// parseKeyExpiry tra ve (mode, expiresAt, secondsLeft).
//   mode: "none" | "active" | "paused"
//   now:  unix giay hien tai.
func parseKeyExpiry(name string, now int64) (string, int64, int64) {
	if m := expPauseRe.FindStringSubmatch(name); m != nil {
		secs, _ := strconv.ParseInt(m[1], 10, 64)
		return "paused", 0, secs
	}
	// exp date phai thu TRUOC exp unix (vi \d+ se bat "2026" trong 2026-01-01).
	if m := expDateReSF.FindStringSubmatch(name); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		exp := time.Date(y, time.Month(mo), d, 23, 59, 59, 0, time.UTC).Unix()
		return "active", exp, exp - now
	}
	if m := expUnixReSF.FindStringSubmatch(name); m != nil {
		exp, _ := strconv.ParseInt(m[1], 10, 64)
		return "active", exp, exp - now
	}
	return "none", 0, 0
}

// stripKeyExpiryMarkers bo cac marker #exp/#pause khoi ten hien thi.
func stripKeyExpiryMarkers(name string) string {
	return strings.TrimSpace(expStripReSF.ReplaceAllString(name, ""))
}
