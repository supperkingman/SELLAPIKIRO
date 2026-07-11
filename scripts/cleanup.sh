#!/usr/bin/env bash
# cleanup.sh - Don rac thong minh cho Kiro-Go. AN TOAN TUYET DOI voi bo dem
# token/credit: script nay KHONG BAO GIO cham vao data/config.json (noi luu
# tokensUsed/creditsUsed cua tung key). No chi:
#   1. Cat bot dong cu cua DUNG 3 file log (chi dinh dich danh, khong dung wildcard)
#   2. Don Docker build cache cu (giu cache 72h gan nhat)
#   3. CANH BAO khi o dia gan day (chi DOC df, khong xoa gi)
#
# Chay 1 lan/ngay qua cron (xem setup-cron.sh).
set -euo pipefail
cd "$(dirname "$0")/.."

# So dong toi da giu lai cho moi file log.
LOG_KEEP_LINES="${LOG_KEEP_LINES:-2000}"
# Nguong canh bao o dia (phan tram).
DISK_WARN_PCT="${DISK_WARN_PCT:-85}"

now() { date '+%Y-%m-%d %H:%M:%S'; }
echo "[$(now)] === Bat dau don rac ==="

# --- Nap bien .env cho canh bao (Telegram/Discord), giong healthcheck ---
if [ -f .env ]; then
  while IFS= read -r line; do
    case "$line" in
      ''|\#*) continue ;;
      *=*)
        k="${line%%=*}"
        case "$k" in *[!A-Za-z0-9_]*) continue ;; esac
        if [ -z "$(eval "echo \${$k:-}")" ]; then export "$k=${line#*=}"; fi
        ;;
    esac
  done < .env
fi

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

# ============================================================
# 1. CAT LOG - chi dinh dich danh tung file, KHONG dung wildcard.
#    Tuyet doi khong lien quan config.json.
# ============================================================
trim_log() {
  local f="$1"
  [ -f "$f" ] || return 0
  local lines
  lines=$(wc -l < "$f" 2>/dev/null || echo 0)
  if [ "$lines" -gt "$LOG_KEEP_LINES" ]; then
    # Giu LOG_KEEP_LINES dong cuoi. Ghi ra file tam roi thay the (an toan,
    # giu nguyen inode-free: dung mv trong cung thu muc data/).
    local tmp="${f}.trim.$$"
    tail -n "$LOG_KEEP_LINES" "$f" > "$tmp" && mv "$tmp" "$f"
    echo "[$(now)] Da cat $f: $lines -> $LOG_KEEP_LINES dong"
  else
    echo "[$(now)] $f: $lines dong (chua can cat)"
  fi
}

# Danh sach TUONG MINH - chi 3 file log nay, khong gi khac.
trim_log "data/health.log"
trim_log "data/backup.log"
trim_log "data/key-expiry.log"

# ============================================================
# 2. DON DOCKER BUILD CACHE cu (giu cache 72h gan nhat de build sau nhanh).
#    Chi anh huong cache build cua Docker, khong dung toi container/volume/data.
# ============================================================
if command -v docker >/dev/null 2>&1; then
  echo "[$(now)] Don Docker build cache cu hon 72h..."
  docker builder prune -f --filter "until=72h" >/dev/null 2>&1 \
    && echo "[$(now)] Da don build cache." \
    || echo "[$(now)] (bo qua don build cache)"
  # Don them image mo coi (dangling), khong dung --all de tranh xoa image dang dung.
  docker image prune -f >/dev/null 2>&1 || true
fi

# ============================================================
# 3. CANH BAO O DIA - chi DOC, khong xoa gi.
# ============================================================
DISK_PCT=$(df -P . | awk 'NR==2 {gsub("%","",$5); print $5}')
echo "[$(now)] O dia dang dung: ${DISK_PCT}%"
if [ -n "$DISK_PCT" ] && [ "$DISK_PCT" -ge "$DISK_WARN_PCT" ]; then
  send_alert "[Kiro-Go] CANH BAO: o dia da dung ${DISK_PCT}% (nguong ${DISK_WARN_PCT}%). Can don dep thu cong."
fi

echo "[$(now)] === Don rac hoan tat ==="
