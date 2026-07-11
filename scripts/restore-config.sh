#!/usr/bin/env bash
# restore-config.sh - Khoi phuc data/config.json tu ban backup.
#
# Dung khi config.json bi hong/mat hoac can quay ve trang thai truoc.
#
# Cach dung:
#   ./restore-config.sh                 # liet ke cac ban backup co san
#   ./restore-config.sh latest          # khoi phuc ban moi nhat
#   ./restore-config.sh <ten-file>      # khoi phuc 1 ban cu the
#
# An toan:
#   - Tu sao luu config.json HIEN TAI truoc khi ghi de (de undo).
#   - Kiem tra JSON hop le sau khi giai nen.
#   - Hoi xac nhan truoc khi ghi de (tru khi dat FORCE=1).
set -euo pipefail
cd "$(dirname "$0")/.."

CONFIG="data/config.json"
BACKUP_DIR="${BACKUP_DIR:-data/backups}"
ARG="${1:-}"

list_backups() {
  if ! ls -1 "$BACKUP_DIR"/config-*.json.gz >/dev/null 2>&1; then
    echo "Khong co ban backup nao trong $BACKUP_DIR"
    exit 1
  fi
  echo "Cac ban backup (moi nhat truoc):"
  ls -1t "$BACKUP_DIR"/config-*.json.gz | while read -r f; do
    echo "  $(basename "$f")  ($(wc -c < "$f") bytes)"
  done
}

if [ -z "$ARG" ]; then
  list_backups
  echo ""
  echo "Chay lai voi: restore-config.sh latest   HOAC   restore-config.sh <ten-file>"
  exit 0
fi

# Chon file nguon
if [ "$ARG" = "latest" ]; then
  SRC="$(ls -1t "$BACKUP_DIR"/config-*.json.gz 2>/dev/null | head -1 || true)"
  [ -z "$SRC" ] && { echo "Khong tim thay ban backup nao"; exit 1; }
else
  SRC="$BACKUP_DIR/$ARG"
  [ -f "$SRC" ] || SRC="$ARG"   # cho phep duong dan tuyet doi
  [ -f "$SRC" ] || { echo "Khong thay file: $ARG"; exit 1; }
fi

echo "==> Se khoi phuc tu: $SRC"

# Giai nen ra file tam roi kiem tra JSON
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT
gunzip -c "$SRC" > "$TMP"

if command -v python3 >/dev/null 2>&1; then
  if ! python3 -c "import json,sys; json.load(open('$TMP'))" 2>/dev/null; then
    echo "LOI: ban backup khong phai JSON hop le - huy khoi phuc"
    exit 1
  fi
  echo "    JSON hop le."
fi

# Xac nhan
if [ "${FORCE:-0}" != "1" ]; then
  printf "Ghi de %s? (go 'yes' de tiep tuc): " "$CONFIG"
  read -r ans
  [ "$ans" = "yes" ] || { echo "Da huy."; exit 0; }
fi

# Sao luu ban hien tai truoc khi ghi de (de undo)
if [ -f "$CONFIG" ]; then
  SAFETY="$BACKUP_DIR/pre-restore-$(date +%Y%m%d-%H%M%S).json.gz"
  mkdir -p "$BACKUP_DIR"
  gzip -c "$CONFIG" > "$SAFETY"
  echo "    Da sao luu ban hien tai -> $SAFETY"
fi

cp "$TMP" "$CONFIG"
echo "==> Da khoi phuc xong: $CONFIG"
echo "    Khoi dong lai de ap dung:  docker compose restart kiro-go keycheck"
