#!/usr/bin/env bash
# check-key-expiry.sh - Tu dong TAT (khong xoa) cac API key da het han.
#
# Cach hoat dong:
#   - Doc danh sach key qua admin API cua kiro-go.
#   - Han su dung duoc ma hoa trong TEN key dang: "#exp=YYYY-MM-DD" (do UI them khi tao).
#   - Neu qua han (het ngay do, tinh 23:59:59) va key con enabled -> PUT enabled=false.
#   - KHONG BAO GIO xoa key.
#
# Chay dinh ky boi cron (xem scripts/setup-cron.sh). Chay tu thu muc repo (co .env).
set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_DIR"

# --- Doc cau hinh ---
[ -f .env ] || { echo "!! Thieu .env"; exit 1; }
# shellcheck disable=SC1091
set +u; . ./.env; set -u
KIRO_PORT="${KIRO_PORT:-8080}"
ADMIN="${ADMIN_PASSWORD:-}"
[ -n "$ADMIN" ] || { echo "!! Thieu ADMIN_PASSWORD trong .env"; exit 1; }

BASE="http://127.0.0.1:${KIRO_PORT}/admin/api"
NOW=$(date +%s)

# --- Lay danh sach key (JSON) ---
LIST="$(curl -s "$BASE/api-keys" -H "X-Admin-Password: $ADMIN")" || { echo "!! Khong goi duoc API"; exit 1; }

# --- Duyet tung key bang python (co san tren VPS) ---
echo "$LIST" | python3 - "$NOW" <<'PYEOF' | while IFS='|' read -r id name; do
import sys, json, re, datetime
now = int(sys.argv[1])
data = json.load(sys.stdin)
keys = data.get("apiKeys", data if isinstance(data, list) else [])
for k in keys:
    if not k.get("enabled", False):
        continue
    name = k.get("name", "") or ""
    # Key dang tam dung (#pause=) -> dong ho dong bang, KHONG bao gio het han.
    if re.search(r'#pause=\d+', name):
        continue
    expiry = None
    # Dinh dang moi: #exp=<unix giay>. Thu TRUOC dinh dang ngay.
    m = re.search(r'#exp=(\d{4}-\d{2}-\d{2})', name)
    if m:
        try:
            d = datetime.datetime.strptime(m.group(1), "%Y-%m-%d")
            # Het han vao cuoi ngay do (23:59:59) theo gio local server.
            expiry = int(d.timestamp()) + 86399
        except Exception:
            continue
    else:
        m = re.search(r'#exp=(\d+)', name)
        if m:
            expiry = int(m.group(1))
    if expiry is None:
        continue
    if now > expiry:
        print(str(k.get("id", "")) + "|" + name)
PYEOF
  [ -n "$id" ] || continue
  echo "==> Tat key het han: $name ($id)"
  curl -s -o /dev/null -X PUT "$BASE/api-keys/$id" \
    -H "X-Admin-Password: $ADMIN" -H "Content-Type: application/json" \
    -d '{"enabled":false}' && echo "    da tat." || echo "    !! tat that bai."
done

echo "==> Kiem tra han key xong: $(date)"
