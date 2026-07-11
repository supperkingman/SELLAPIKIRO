#!/usr/bin/env bash
# setup-ols-proxy.sh - Tu dong cau hinh OpenLiteSpeed reverse proxy cho site.
#
# Tu tim file vhosts.conf theo ten mien, backup, chen extprocessor + context
# tro toi kiro-go (KIRO_PORT) va keycheck (KEYCHECK_PORT). Doc cong tu .env.
#
# Chay bang root/sudo:  sudo bash scripts/setup-ols-proxy.sh <domain>
# Vi du:                sudo bash scripts/setup-ols-proxy.sh api.mmodiary.com
set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_DIR"

# --- Doc cong tu .env (mac dinh 8080/8081) ---
KIRO_PORT=8080
KEYCHECK_PORT=8081
if [ -f .env ]; then
  # shellcheck disable=SC1091
  set +u; . ./.env; set -u
  KIRO_PORT="${KIRO_PORT:-8080}"
  KEYCHECK_PORT="${KEYCHECK_PORT:-8081}"
fi

DOMAIN="${1:-}"
if [ -z "$DOMAIN" ]; then
  # FlashPanel dat ten thu muc site TRUNG ten mien (vd .../api.mmodiary.com).
  # -> Tu lay ten thu muc lam domain neu no giong ten mien (co dau cham).
  CAND="$(basename "$REPO_DIR")"
  if echo "$CAND" | grep -q '\.'; then
    DOMAIN="$CAND"
    echo "==> Tu nhan domain tu ten thu muc: $DOMAIN"
  else
    echo "!! Khong tu nhan duoc domain (thu muc '$CAND' khong giong ten mien)."
    echo "   Truyen tay: sudo bash scripts/setup-ols-proxy.sh <domain>"
    exit 1
  fi
fi
if [ "$(id -u)" -ne 0 ]; then
  echo "!! Can chay bang root: sudo bash scripts/setup-ols-proxy.sh $DOMAIN"
  exit 1
fi

# Ten external app rieng theo site de nhieu site khong dung ten (dung SITE_NAME).
APP_KIRO="${SITE_NAME:-kiro}go"
APP_KC="${SITE_NAME:-kiro}kc"

echo "==> Tim vhost config cho $DOMAIN ..."
# Tim file vhosts.conf co chua ten mien, hoac thu muc trung ten mien.
VHOST=""
for f in /usr/local/lsws/conf/vhosts/*/vhosts.conf; do
  [ -f "$f" ] || continue
  if grep -q "$DOMAIN" "$f" 2>/dev/null; then VHOST="$f"; break; fi
done
# Neu khong grep thay, thu tim theo thu muc chua file (fallback: file duy nhat).
if [ -z "$VHOST" ]; then
  cnt=$(ls -1 /usr/local/lsws/conf/vhosts/*/vhosts.conf 2>/dev/null | wc -l)
  if [ "$cnt" = "1" ]; then
    VHOST=$(ls -1 /usr/local/lsws/conf/vhosts/*/vhosts.conf)
    echo "   (khong grep thay ten mien, dung file duy nhat: $VHOST)"
  fi
fi
if [ -z "$VHOST" ] || [ ! -f "$VHOST" ]; then
  echo "!! Khong tim thay vhosts.conf cho $DOMAIN."
  echo "   Kiem tra: ls /usr/local/lsws/conf/vhosts/*/vhosts.conf"
  exit 1
fi
echo "   Tim thay: $VHOST"

# --- Idempotent: neu da chen roi thi bo qua ---
if grep -q "extprocessor $APP_KIRO" "$VHOST" 2>/dev/null; then
  echo "==> Proxy da duoc cau hinh truoc do (bo qua chen)."
  echo "   Neu muon cau hinh lai, xoa cac khoi '$APP_KIRO'/'$APP_KC' trong $VHOST roi chay lai."
  exit 0
fi

# --- Backup ---
BAK="${VHOST}.bak-$(date +%Y%m%d-%H%M%S)"
cp "$VHOST" "$BAK"
echo "==> Da backup: $BAK"

# --- Chen extprocessor + context vao CUOI file ---
# Context cu the (/check-key, /api/check-key) dat truoc; context "/" dat cuoi cung.
cat >> "$VHOST" <<OLSBLOCK

# ==== KIRO-GO REVERSE PROXY (tu dong them boi setup-ols-proxy.sh) ====
extprocessor $APP_KIRO {
  type                    proxy
  address                 127.0.0.1:$KIRO_PORT
  maxConns                100
  pcKeepAliveTimeout      3600
  initTimeout             60
  retryTimeout            0
  respBuffer              0
}

extprocessor $APP_KC {
  type                    proxy
  address                 127.0.0.1:$KEYCHECK_PORT
  maxConns                50
  pcKeepAliveTimeout      60
  initTimeout             60
  retryTimeout            0
  respBuffer              0
}

context /api/check-key {
  type                    proxy
  handler                 $APP_KC
  addDefaultCharset       off
}

context /check-key {
  type                    proxy
  handler                 $APP_KC
  addDefaultCharset       off
}

context / {
  type                    proxy
  handler                 $APP_KIRO
  addDefaultCharset       off
}
# ==== HET KIRO-GO REVERSE PROXY ====
OLSBLOCK

# --- Kiem tra can bang { } truoc khi restart (tranh lam sap OLS) ---
OPEN=$(grep -o '{' "$VHOST" | wc -l)
CLOSE=$(grep -o '}' "$VHOST" | wc -l)
if [ "$OPEN" != "$CLOSE" ]; then
  echo "!! LOI: dau ngoac khong can bang ({ =$OPEN, } =$CLOSE). Khoi phuc backup."
  cp "$BAK" "$VHOST"
  exit 1
fi
echo "==> Dau ngoac can bang ({ =$OPEN, } =$CLOSE). OK."

# --- Restart OLS ---
echo "==> Restart OpenLiteSpeed ..."
/usr/local/lsws/bin/lswsctrl restart
sleep 3

# --- Kiem tra nhanh qua HTTPS noi bo ---
echo "==> Kiem tra endpoint (qua HTTPS noi bo):"
for path in /admin /check-key; do
  code=$(curl -sk -o /dev/null -w "%{http_code}" "https://127.0.0.1$path" -H "Host: $DOMAIN" || echo "ERR")
  echo "   $path -> $code"
done

echo "==> Xong. Neu /admin tra 200 la proxy da hoat dong."
echo "   Backup vhost cu: $BAK"
