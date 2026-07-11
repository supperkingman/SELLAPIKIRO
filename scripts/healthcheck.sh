#!/usr/bin/env bash
# healthcheck.sh - Giam sat suc khoe he thong Kiro-Go va canh bao chu dong.
#
# Kiem tra:
#   1. Service co song khong (HTTP /admin tra ve OK)
#   2. So tai khoan kha dung trong pool (canh bao neu = 0)
#   3. (tuy chon) gui canh bao qua webhook Telegram / Discord khi co su co
#
# Chay dinh ky qua cron, vi du moi 5 phut:
#   */5 * * * * /duong-dan/scripts/healthcheck.sh >> /duong-dan/data/health.log 2>&1
#
# Bien moi truong (dat trong .env hoac moi truong):
#   BASE_URL          (mac dinh http://127.0.0.1:8080)
#   ADMIN_PASSWORD    (doc tu .env neu khong dat)
#   ALERT_WEBHOOK     URL webhook (Discord) HOAC dung TELEGRAM_* ben duoi
#   TELEGRAM_TOKEN    token bot Telegram
#   TELEGRAM_CHAT_ID  chat id nhan canh bao
#   STATE_FILE        file luu trang thai de tranh spam canh bao (mac dinh data/.health_state)
set -euo pipefail
cd "$(dirname "$0")/.."

BASE_URL="${BASE_URL:-http://127.0.0.1:8080}"
STATE_FILE="${STATE_FILE:-data/.health_state}"

# Nap bien tu .env (ADMIN_PASSWORD, TELEGRAM_*, ALERT_WEBHOOK...). Cron chay tu
# REPO_DIR nen .env o day. Chi nhan dong KEY=VALUE hop le, bo qua comment.
if [ -f .env ]; then
  while IFS= read -r line; do
    case "$line" in
      ''|\#*) continue ;;
      *=*)
        k="${line%%=*}"
        # Chi nhan ten bien hop le (chu, so, gach duoi)
        case "$k" in
          *[!A-Za-z0-9_]*) continue ;;
        esac
        # Khong ghi de bien da co tu moi truong (vd dat truc tiep khi test)
        if [ -z "$(eval "echo \${$k:-}")" ]; then
          export "$k=${line#*=}"
        fi
        ;;
    esac
  done < .env
fi

now() { date '+%Y-%m-%d %H:%M:%S'; }

# Gui canh bao (chi khi trang thai THAY DOI, tranh spam)
send_alert() {
  local msg="$1"
  echo "[$(now)] ALERT: $msg"
  if [ -n "${ALERT_WEBHOOK:-}" ]; then
    curl -fsS -m 10 -H "Content-Type: application/json" \
      -d "{\"content\": $(printf '%s' "$msg" | python3 -c 'import json,sys;print(json.dumps(sys.stdin.read()))')}" \
      "$ALERT_WEBHOOK" >/dev/null 2>&1 || echo "[$(now)] (khong gui duoc webhook)"
  fi
  if [ -n "${TELEGRAM_TOKEN:-}" ] && [ -n "${TELEGRAM_CHAT_ID:-}" ]; then
    curl -fsS -m 10 \
      --data-urlencode "chat_id=${TELEGRAM_CHAT_ID}" \
      --data-urlencode "text=${msg}" \
      "https://api.telegram.org/bot${TELEGRAM_TOKEN}/sendMessage" >/dev/null 2>&1 \
      || echo "[$(now)] (khong gui duoc telegram)"
  fi
}

# Doc trang thai truoc do (OK / DOWN)
prev_state="OK"
[ -f "$STATE_FILE" ] && prev_state="$(cat "$STATE_FILE" 2>/dev/null || echo OK)"

new_state="OK"
problems=""

# 1. Service song?
http_code="$(curl -fsS -m 10 -o /dev/null -w '%{http_code}' "$BASE_URL/admin" 2>/dev/null || echo 000)"
if [ "$http_code" = "000" ]; then
  new_state="DOWN"
  problems="Service KHONG phan hoi tai $BASE_URL"
else
  echo "[$(now)] Service OK (HTTP $http_code)"
  # 2. Kiem tra pool tai khoan (neu co mat khau admin)
  if [ -n "${ADMIN_PASSWORD:-}" ] && command -v python3 >/dev/null 2>&1; then
    accounts_json="$(curl -fsS -m 10 -H "X-Admin-Password: $ADMIN_PASSWORD" "$BASE_URL/admin/api/accounts" 2>/dev/null || echo '')"
    if [ -n "$accounts_json" ]; then
      summary="$(printf '%s' "$accounts_json" | python3 - <<'PY'
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    print("PARSE_ERR 0 0"); sys.exit()
# Endpoint /admin/api/accounts tra ve LIST truc tiep; phong truong hop boc trong dict.
if isinstance(d, dict):
    accs = d.get("accounts", [])
elif isinstance(d, list):
    accs = d
else:
    accs = []
total = len(accs)
disabled = sum(1 for a in accs if not a.get("enabled", True))
avail = total - disabled
print(f"OK {total} {avail}")
PY
)"
      set -- $summary
      tag="${1:-OK}"; total="${2:-0}"; avail="${3:-0}"
      echo "[$(now)] Tai khoan: tong=$total, kha dung=$avail"
      if [ "$total" -gt 0 ] && [ "$avail" -eq 0 ]; then
        new_state="DEGRADED"
        problems="Tat ca $total tai khoan deu khong kha dung (bi tat/loi)"
      fi
    fi
  fi
fi

# So sanh trang thai -> chi canh bao khi co thay doi
if [ "$new_state" != "OK" ] && [ "$prev_state" = "OK" ]; then
  send_alert "[Kiro-Go] SU CO ($new_state): $problems"
elif [ "$new_state" = "OK" ] && [ "$prev_state" != "OK" ]; then
  send_alert "[Kiro-Go] DA KHOI PHUC: service hoat dong binh thuong tro lai"
fi

echo "$new_state" > "$STATE_FILE"
[ "$new_state" = "OK" ] || exit 1
