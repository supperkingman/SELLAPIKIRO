#!/usr/bin/env bash
# setup-ols-proxy.sh - Tu dong cau hinh OpenLiteSpeed reverse proxy cho site.
#
# Tu tim file vhosts.conf theo ten mien, backup, chen extprocessor + context
# tro toi kiro-go (KIRO_PORT), keycheck (KEYCHECK_PORT), keyadmin (KEYADMIN_PORT).
# Doc cong tu .env.
#
# Chay bang root/sudo:  sudo bash scripts/setup-ols-proxy.sh <domain>
# Vi du:                sudo bash scripts/setup-ols-proxy.sh api.mmodiary.com
set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_DIR"

# --- Doc cong tu .env (mac dinh 8080/8081/8082) ---
KIRO_PORT=8080
KEYCHECK_PORT=8081
KEYADMIN_PORT=8082
if [ -f .env ]; then
  # shellcheck disable=SC1091
  set +u; . ./.env; set -u
  KIRO_PORT="${KIRO_PORT:-8080}"
  KEYCHECK_PORT="${KEYCHECK_PORT:-8081}"
  KEYADMIN_PORT="${KEYADMIN_PORT:-8082}"
fi

DOMAIN="${1:-}"
if [ -z "$DOMAIN" ]; then
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

APP_KIRO="${SITE_NAME:-kiro}go"
APP_KC="${SITE_NAME:-kiro}kc"
APP_KA="${SITE_NAME:-kiro}ka"

echo "==> Tim vhost config cho $DOMAIN ..."
VHOST=""
for f in /usr/local/lsws/conf/vhosts/*/vhosts.conf; do
  [ -f "$f" ] || continue
  if grep -q "$DOMAIN" "$f" 2>/dev/null; then VHOST="$f"; break; fi
done
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

insert_keyadmin_before_root() {
  local port="$1"
  local app="$2"
  local tmp="${VHOST}.tmp-ka"
  awk -v app="$app" -v port="$port" '
    BEGIN { inserted=0 }
    /^context[[:space:]]+\/[[:space:]]*\{/ && !inserted {
      print ""
      print "# ---- keyadmin (bot Telegram) — auto by setup-ols-proxy.sh ----"
      print "extprocessor " app " {"
      print "  type                    proxy"
      print "  address                 127.0.0.1:" port
      print "  maxConns                20"
      print "  pcKeepAliveTimeout      60"
      print "  initTimeout             30"
      print "  retryTimeout            0"
      print "  respBuffer              0"
      print "}"
      print ""
      print "context /keyadmin/ {"
      print "  type                    proxy"
      print "  handler                 " app
      print "  addDefaultCharset       off"
      print "}"
      print ""
      inserted=1
    }
    { print }
    END {
      if (!inserted) {
        print ""
        print "extprocessor " app " {"
        print "  type                    proxy"
        print "  address                 127.0.0.1:" port
        print "  maxConns                20"
        print "  pcKeepAliveTimeout      60"
        print "  initTimeout             30"
        print "  retryTimeout            0"
        print "  respBuffer              0"
        print "}"
        print "context /keyadmin/ {"
        print "  type                    proxy"
        print "  handler                 " app
        print "  addDefaultCharset       off"
        print "}"
      }
    }
  ' "$VHOST" > "$tmp"
  mv "$tmp" "$VHOST"
}

balance_check_or_restore() {
  local bak="$1"
  local OPEN CLOSE
  OPEN=$(grep -o '{' "$VHOST" | wc -l)
  CLOSE=$(grep -o '}' "$VHOST" | wc -l)
  if [ "$OPEN" != "$CLOSE" ]; then
    echo "!! LOI: dau ngoac khong can bang ({ =$OPEN, } =$CLOSE). Khoi phuc backup."
    cp "$bak" "$VHOST"
    exit 1
  fi
  echo "==> Dau ngoac can bang ({ =$OPEN, } =$CLOSE). OK."
}

restart_and_probe() {
  echo "==> Restart OpenLiteSpeed ..."
  /usr/local/lsws/bin/lswsctrl restart
  sleep 3
  echo "==> Kiem tra endpoint (qua HTTPS noi bo):"
  for path in /admin /check-key /keyadmin/healthz; do
    code=$(curl -sk -o /dev/null -w "%{http_code}" "https://127.0.0.1$path" -H "Host: $DOMAIN" || echo "ERR")
    echo "   $path -> $code"
  done
}

# --- Da co keyadmin? ---
if grep -qE "extprocessor[[:space:]]+$APP_KA|context[[:space:]]+/keyadmin" "$VHOST" 2>/dev/null; then
  echo "==> keyadmin proxy da co trong vhost (bo qua)."
  exit 0
fi

# --- Proxy kiro da co nhung thieu keyadmin -> chi bo sung ---
if grep -q "extprocessor $APP_KIRO" "$VHOST" 2>/dev/null; then
  echo "==> Proxy kiro da co, dang BO SUNG keyadmin (port $KEYADMIN_PORT) ..."
  BAK="${VHOST}.bak-keyadmin-$(date +%Y%m%d-%H%M%S)"
  cp "$VHOST" "$BAK"
  echo "==> Da backup: $BAK"
  insert_keyadmin_before_root "$KEYADMIN_PORT" "$APP_KA"
  balance_check_or_restore "$BAK"
  restart_and_probe
  echo "==> Xong (upgrade keyadmin). /keyadmin/healthz ky vong 200 (body: ok)."
  echo "   Backup: $BAK"
  exit 0
fi

# --- Chen full lan dau ---
BAK="${VHOST}.bak-$(date +%Y%m%d-%H%M%S)"
cp "$VHOST" "$BAK"
echo "==> Da backup: $BAK"

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

extprocessor $APP_KA {
  type                    proxy
  address                 127.0.0.1:$KEYADMIN_PORT
  maxConns                20
  pcKeepAliveTimeout      60
  initTimeout             30
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

# Bot Telegram: https://DOMAIN/keyadmin/api/... + Bearer KEYADMIN_TOKEN
context /keyadmin/ {
  type                    proxy
  handler                 $APP_KA
  addDefaultCharset       off
}

context / {
  type                    proxy
  handler                 $APP_KIRO
  addDefaultCharset       off
}
# ==== HET KIRO-GO REVERSE PROXY ====
OLSBLOCK

balance_check_or_restore "$BAK"
restart_and_probe
echo "==> Xong. /keyadmin/healthz ky vong 200 (body: ok)."
echo "   Backup vhost cu: $BAK"
