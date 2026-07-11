#!/usr/bin/env bash
# setup-cron.sh - Cai dat lich tu dong cho backup + healthcheck (idempotent).
#
# Chay 1 lan tren VPS:
#   bash scripts/setup-cron.sh
#
# Se them 2 dong vao crontab cua user hien tai:
#   - Backup config moi 6 tieng
#   - Healthcheck moi 5 phut
#
# Chay lai script se KHONG tao trung (tu nhan biet qua marker).
set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
MARKER="# KIRO-GO-CRON"

BACKUP_LINE="0 */6 * * * cd $REPO_DIR && bash scripts/backup-config.sh >> data/backup.log 2>&1 $MARKER"
HEALTH_LINE="*/5 * * * * cd $REPO_DIR && bash scripts/healthcheck.sh >> data/health.log 2>&1 $MARKER"
EXPIRY_LINE="*/10 * * * * cd $REPO_DIR && bash scripts/check-key-expiry.sh >> data/key-expiry.log 2>&1 $MARKER"
CLEANUP_LINE="0 3 * * * cd $REPO_DIR && bash scripts/cleanup.sh >> data/cleanup.log 2>&1 $MARKER"

echo "==> Cau hinh cron cho thu muc: $REPO_DIR"

# Lay crontab hien tai (neu chua co thi rong), bo cac dong cua chung ta truoc do
current="$(crontab -l 2>/dev/null | grep -v "$MARKER" || true)"

# Ghep lai
{
  printf '%s\n' "$current"
  printf '%s\n' "$BACKUP_LINE"
  printf '%s\n' "$HEALTH_LINE"
  printf '%s\n' "$EXPIRY_LINE"
  printf '%s\n' "$CLEANUP_LINE"
} | sed '/^$/d' | crontab -

echo "==> Da cai dat. Crontab hien tai:"
crontab -l | grep "$MARKER" || true
echo ""
echo "Backup -> data/backups/   (giu 30 ban moi nhat, 6 tieng/lan)"
echo "Health -> data/health.log (5 phut/lan)"
echo "Key han -> data/key-expiry.log (tu tat key het han, 10 phut/lan)"
echo "Don rac -> data/cleanup.log (cat log + don cache + canh bao dia, 3h sang/ngay)"
echo ""
echo "De canh bao qua Telegram/Discord, them vao .env:"
echo "  TELEGRAM_TOKEN=...    TELEGRAM_CHAT_ID=...   (hoac)   ALERT_WEBHOOK=https://discord..."
echo "Roi sua healthcheck doc .env: cron da chay tu REPO_DIR nen .env duoc nhan."
