#!/usr/bin/env bash
# backup-config.sh - Sao luu tu dong data/config.json (chua API key + tai khoan Kiro).
#
# config.json la "trai tim" cua he thong - mat la mat toan bo key va tai khoan.
# Kiro-Go khong tu backup, nen dung script nay (chay tay hoac qua cron).
#
# Dac diem:
#   - Backup co nen gzip, dat ten theo thoi gian.
#   - Tu xoay vong: chi giu N ban moi nhat (mac dinh 30).
#   - Kiem tra JSON hop le truoc khi luu (tranh backup file hong).
#   - Khong in noi dung key ra man hinh / log.
#
# Cron vi du (backup moi 6 tieng):
#   0 */6 * * * /duong-dan/scripts/backup-config.sh >> /duong-dan/data/backup.log 2>&1
set -euo pipefail

cd "$(dirname "$0")/.."   # ve thu muc goc repo

CONFIG="data/config.json"
BACKUP_DIR="${BACKUP_DIR:-data/backups}"
KEEP="${KEEP:-30}"        # so ban backup giu lai

if [ ! -f "$CONFIG" ]; then
  echo "[backup] LOI: khong thay $CONFIG"
  exit 1
fi

# Kiem tra JSON hop le (dung python neu co, khong thi bo qua buoc nay)
if command -v python3 >/dev/null 2>&1; then
  if ! python3 -c "import json,sys; json.load(open('$CONFIG'))" 2>/dev/null; then
    echo "[backup] LOI: $CONFIG khong phai JSON hop le - bo qua de tranh backup file hong"
    exit 1
  fi
fi

mkdir -p "$BACKUP_DIR"
STAMP="$(date +%Y%m%d-%H%M%S)"
OUT="$BACKUP_DIR/config-$STAMP.json.gz"

gzip -c "$CONFIG" > "$OUT"
echo "[backup] Da luu: $OUT ($(wc -c < "$OUT") bytes)"

# Xoay vong: xoa cac ban cu, chi giu KEEP ban moi nhat
COUNT=$(ls -1 "$BACKUP_DIR"/config-*.json.gz 2>/dev/null | wc -l)
if [ "$COUNT" -gt "$KEEP" ]; then
  ls -1t "$BACKUP_DIR"/config-*.json.gz | tail -n +"$((KEEP+1))" | while read -r old; do
    rm -f "$old"
    echo "[backup] Da xoa ban cu: $old"
  done
fi

echo "[backup] Hoan tat. Tong so ban hien co: $(ls -1 "$BACKUP_DIR"/config-*.json.gz 2>/dev/null | wc -l)"
