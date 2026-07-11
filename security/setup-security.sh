#!/usr/bin/env bash
# setup-security.sh - Cai dat fail2ban chong brute-force admin + do key.
#
# Chay tren VPS voi quyen root (hoac sudo):
#   sudo bash security/setup-security.sh
#
# Script se:
#   1. Copy filter + jail vao /etc/fail2ban/
#   2. Kiem tra duong dan access log OLS ton tai
#   3. Test regex khop log that
#   4. Reload fail2ban va hien trang thai
set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
SEC_DIR="$REPO_DIR/security/fail2ban"
OLS_LOG="/usr/local/lsws/logs/access.log"

if [ "$(id -u)" -ne 0 ]; then
  echo "!! Can chay bang root: sudo bash security/setup-security.sh"
  exit 1
fi

if ! command -v fail2ban-client >/dev/null 2>&1; then
  echo "!! fail2ban chua cai. Cai bang: apt-get install -y fail2ban"
  exit 1
fi

echo "==> [1/5] Kiem tra access log OLS: $OLS_LOG"
if [ ! -f "$OLS_LOG" ]; then
  echo "!! Khong thay $OLS_LOG. Sua logpath trong security/fail2ban/jail.d/kiro.local cho dung."
  exit 1
fi

echo "==> [2/5] Copy filter vao /etc/fail2ban/filter.d/"
cp "$SEC_DIR/filter.d/kiro-admin.conf"    /etc/fail2ban/filter.d/
cp "$SEC_DIR/filter.d/kiro-checkkey.conf" /etc/fail2ban/filter.d/

echo "==> [3/5] Copy jail vao /etc/fail2ban/jail.d/"
cp "$SEC_DIR/jail.d/kiro.local" /etc/fail2ban/jail.d/

echo "==> [4/5] Test regex admin khop log that (neu co du lieu)"
fail2ban-regex "$OLS_LOG" /etc/fail2ban/filter.d/kiro-admin.conf 2>/dev/null | grep -E "Failregex: [0-9]+ total" || true

echo "==> [5/5] Reload fail2ban"
systemctl restart fail2ban
sleep 2
echo ""
echo "===== TRANG THAI JAIL ====="
fail2ban-client status kiro-admin 2>/dev/null || echo "(jail kiro-admin chua active - kiem tra log)"
fail2ban-client status kiro-checkkey 2>/dev/null || echo "(jail kiro-checkkey chua active)"
echo ""
echo "Xong. Xem chi tiet: fail2ban-client status kiro-admin"
echo "Go ban 1 IP:        fail2ban-client set kiro-admin unbanip <IP>"
