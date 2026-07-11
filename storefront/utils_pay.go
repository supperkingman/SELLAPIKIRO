// utils_pay.go - helper dung chung cho cac cong thanh toan.
package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
)

// writeJSON ghi response JSON voi status code cho truoc.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// orderCodeRe tim ma don dang KIRO-XXXXXX (6 ky tu chu/so in hoa).
var orderCodeRe = regexp.MustCompile(`KIRO-[A-Z0-9]{6}`)

// extractOrderCode trich ma don tu chuoi bat ky (noi dung CK, memo...).
func extractOrderCode(s string) string {
	return orderCodeRe.FindString(s)
}

// itoa64 doi int64 -> string.
func itoa64(n int64) string {
	return strconv.FormatInt(n, 10)
}
