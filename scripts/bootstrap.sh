#!/usr/bin/env bash
# bootstrap.sh - Cai dat LAN DAU cho 1 site Kiro-Go moi (chay 1 lenh la xong).
#
# Dung sau khi FlashPanel da clone code ve thu muc site:
#     sudo bash scripts/bootstrap.sh <domain>
# Vi du:
#     sudo bash scripts/bootstrap.sh api.mmodiary.com
#
# Script lo TAT CA phan setup thu cong:
#   1. Tao .env (tu sinh mat khau admin manh neu chua co)
#   2. Them user vao group docker (neu can)
#   3. Build + khoi dong container (deploy-hook.sh)
#   4. Cau hinh OpenLiteSpeed reverse proxy (setup-ols-proxy.sh)
#   5. Cai fail2ban chong hack (setup-security.sh)
#   6. Cai cron backup + healthcheck (setup-cron.sh)
#
# Sau lan nay, moi cap nhat chi can push GitHub -> FlashPanel chay deploy-hook.sh.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_DIR"

DOMAIN="${1:-}"
if [ -z "$DOMAIN" ]; then
  # FlashPanel dat ten thu muc site TRUNG ten mien -> tu nhan domain.
  CAND="$(basename "$REPO_DIR")"
  if echo "$CAND" | grep -q '\.'; then DOMAIN="$CAND"; fi
fi
# User that chay docker (nguoi goi sudo), khong phai root.
REAL_USER="${SUDO_USER:-$(whoami)}"

echo "============================================================"
echo "  BOOTSTRAP KIRO-GO  |  domain=${DOMAIN:-<chua dat>}  user=$REAL_USER"
echo "============================================================"

if [ "$(id -u)" -ne 0 ]; then
  echo "!! Can chay bang root/sudo: sudo bash scripts/bootstrap.sh <domain>"
  exit 1
fi

# ---------- 1. Tao .env ----------
echo ""
echo "==> [1/6] Kiem tra .env"
if [ ! -f .env ]; then
  cp .env.example .env
  PASS="$(openssl rand -base64 18 | tr -d '/+=' | cut -c1-20)"
  sed -i "s|^ADMIN_PASSWORD=.*|ADMIN_PASSWORD=${PASS}|" .env
  echo "    Da tao .env voi mat khau admin ngau nhien."
  echo "    >>> MAT KHAU ADMIN (LUU LAI NGAY): ${PASS}"
else
  # Neu ADMIN_PASSWORD con la placeholder -> sinh moi.
  cur="$(grep '^ADMIN_PASSWORD=' .env | cut -d= -f2- || true)"
  if [ -z "$cur" ] || echo "$cur" | grep -q "doi_mat_khau_nay"; then
    PASS="$(openssl rand -base64 18 | tr -d '/+=' | cut -c1-20)"
    sed -i "s|^ADMIN_PASSWORD=.*|ADMIN_PASSWORD=${PASS}|" .env
    echo "    ADMIN_PASSWORD con la placeholder -> da sinh moi."
    echo "    >>> MAT KHAU ADMIN (LUU LAI NGAY): ${PASS}"
  else
    echo "    .env da co san (giu nguyen mat khau hien tai)."
  fi
fi

# ---------- 2. User vao group docker ----------
echo ""
echo "==> [2/6] Kiem tra quyen docker cho user '$REAL_USER'"
if id -nG "$REAL_USER" | grep -qw docker; then
  echo "    User da thuoc group docker."
else
  usermod -aG docker "$REAL_USER"
  echo "    Da them '$REAL_USER' vao group docker."
  echo "    !! LUU Y: user can DANG NHAP LAI (hoac newgrp docker) de co hieu luc."
fi

# ---------- 3. Build + up (deploy-hook) ----------
echo ""
echo "==> [3/6] Build + khoi dong container"
# Chay deploy-hook bang user that de docker khong tao file thuoc root.
sudo -u "$REAL_USER" bash deploy-hook.sh || {
  echo "!! deploy-hook that bai. Neu loi quyen docker: dang nhap lai roi chay:"
  echo "     bash deploy-hook.sh"
  echo "   roi chay tiep: sudo bash scripts/bootstrap.sh $DOMAIN"
  exit 1
}

# ---------- 4. OLS reverse proxy ----------
echo ""
echo "==> [4/6] Cau hinh OpenLiteSpeed reverse proxy"
if [ -z "$DOMAIN" ]; then
  echo "    (Bo qua: chua truyen <domain>. Chay tay sau:"
  echo "       sudo bash scripts/setup-ols-proxy.sh <domain> )"
elif [ -d /usr/local/lsws ]; then
  bash scripts/setup-ols-proxy.sh "$DOMAIN" || echo "    !! OLS proxy loi - xem thong bao tren."
else
  echo "    (Khong thay OpenLiteSpeed - bo qua. Neu dung Nginx, xem nginx-reverse-proxy.conf.example)"
fi

# ---------- 5. fail2ban ----------
echo ""
echo "==> [5/6] Cai fail2ban chong hack"
if [ -f scripts/../security/setup-security.sh ]; then
  bash security/setup-security.sh || echo "    !! fail2ban loi (co the chua cai fail2ban). Bo qua."
else
  echo "    (Khong thay security/setup-security.sh - bo qua)"
fi

# ---------- 6. cron ----------
echo ""
echo "==> [6/6] Cai cron backup + healthcheck"
sudo -u "$REAL_USER" bash scripts/setup-cron.sh || echo "    !! cron loi - xem thong bao tren."

echo ""
echo "============================================================"
echo "  BOOTSTRAP HOAN TAT"
echo "============================================================"
echo "  - Admin:     https://${DOMAIN:-<domain>}/admin"
echo "  - Check key: https://${DOMAIN:-<domain>}/check-key"
echo ""
echo "  Viec tiep theo:"
echo "   1. Dang nhap /admin, doi mat khau neu can."
echo "   2. Them tai khoan Kiro: chay add-account.ps1 tu may Windows."
echo "   3. Bat 'Require API Key' trong Settings neu muon bat buoc khach co key."
echo "============================================================"
