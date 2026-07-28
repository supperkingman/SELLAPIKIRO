#!/usr/bin/env bash
# setup-ai-site.sh - Dung site ai.mmodiary.com voi nhan kiro-go BAN GOC.
#
# Chay tren VPS, trong thu muc site (noi co docker-compose.yml cua site nay).
# Idempotent: chay lai an toan, khong ghi de .env va khong xoa data/.
#
# Viec no lam:
#   1. Clone (hoac cap nhat) Quorinex/Kiro-Go vao kiro-go-upstream/ - KHONG sua
#   2. Copy sidecar keyadmin/ + telegram-bot/ tu repo fork sang
#   3. Copy script het han + dat cron
#   4. Build & khoi dong
#
# Cach dung:
#   bash setup-ai-site.sh /duong/dan/den/repo-fork
set -euo pipefail

UPSTREAM_REPO="https://github.com/Quorinex/Kiro-Go.git"
SITE_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SITE_DIR"

FORK_DIR="${1:-}"
if [ -z "$FORK_DIR" ] || [ ! -d "$FORK_DIR/keyadmin" ]; then
  cat >&2 <<EOF
!! Thieu duong dan den repo fork (noi co keyadmin/ va telegram-bot/).

   Cach dung:
     bash setup-ai-site.sh ~/api.mmodiary.com

   Sidecar khong nam trong ban goc nen phai copy tu fork sang.
EOF
  exit 1
fi
FORK_DIR="$(cd "$FORK_DIR" && pwd)"

echo "==> Site:  $SITE_DIR"
echo "==> Fork:  $FORK_DIR"
echo

# --- 1. Nhan ban goc -------------------------------------------------------
# Giu trong thu muc rieng va KHONG sua gi, de `git pull` tu upstream luon sach.
if [ -d kiro-go-upstream/.git ]; then
  echo "==> Cap nhat kiro-go-upstream (ban goc)"
  git -C kiro-go-upstream fetch origin main --depth 1
  git -C kiro-go-upstream reset --hard origin/main
else
  echo "==> Clone ban goc tu $UPSTREAM_REPO"
  git clone --depth 1 "$UPSTREAM_REPO" kiro-go-upstream
fi
UPSTREAM_SHA="$(git -C kiro-go-upstream rev-parse --short HEAD)"
echo "    ban goc @ $UPSTREAM_SHA"
echo

# --- 2. Sidecar ------------------------------------------------------------
# Copy chu khong symlink: container build context khong di theo symlink ra ngoai.
echo "==> Copy sidecar tu fork"
for d in keyadmin telegram-bot; do
  if [ ! -d "$FORK_DIR/$d" ]; then
    echo "!! Khong tim thay $FORK_DIR/$d" >&2
    exit 1
  fi
  rm -rf "./$d"
  cp -r "$FORK_DIR/$d" "./$d"
  echo "    $d/"
done

mkdir -p scripts
cp "$FORK_DIR/scripts/check-key-expiry.sh" scripts/
chmod +x scripts/check-key-expiry.sh
echo "    scripts/check-key-expiry.sh"
echo

# --- 3. Cau hinh -----------------------------------------------------------
if [ ! -f .env ]; then
  echo "==> Tao .env va sinh mat khau moi"
  cp .env.example .env
  ADMIN_PW="$(openssl rand -hex 32)"
  KEYADMIN_TK="$(openssl rand -hex 32)"
  # Dung | lam dau phan cach de token hex khong bao gio lam vo bieu thuc.
  sed -i "s|^ADMIN_PASSWORD=.*|ADMIN_PASSWORD=$ADMIN_PW|" .env
  sed -i "s|^KEYADMIN_TOKEN=.*|KEYADMIN_TOKEN=$KEYADMIN_TK|" .env
  echo
  echo "    ===================================================="
  echo "    ADMIN_PASSWORD = $ADMIN_PW"
  echo "    ===================================================="
  echo "    LUU LAI NGAY. Chi in mot lan o day."
  echo
else
  echo "==> Da co .env, giu nguyen (khong ghi de mat khau dang dung)"
fi

mkdir -p data

# Bat buoc phai co token bot truoc khi build, vi compose se tu choi parse.
# shellcheck disable=SC1091
set +u; . ./.env; set -u
if [ -z "${TELEGRAM_BOT_TOKEN:-}" ] || [ -z "${TELEGRAM_ADMIN_IDS:-}" ]; then
  cat >&2 <<EOF

!! Chua dien thong tin Telegram trong .env

   Mo .env va dien 2 dong:
     TELEGRAM_BOT_TOKEN=   <- chat @BotFather -> /newbot
     TELEGRAM_ADMIN_IDS=   <- chat @userinfobot -> "Id: ..."

   TELEGRAM_ADMIN_IDS la thu duy nhat ngan nguoi la tao key, nen buoc nay
   khong the bo qua. Dien xong chay lai lenh nay.
EOF
  exit 1
fi

# --- 4. Build & chay -------------------------------------------------------
echo "==> Build & khoi dong"
docker compose build
docker compose up -d
echo

# --- 5. Cron thuc thi het han ---------------------------------------------
# Ban goc KHONG hieu marker #exp trong ten key, nen neu thieu cron nay thi
# /newkey va /addhours chi la trang tri: key hien dung han nhung khong bao gio
# het hieu luc. Day la mau chot, khong phai tuy chon.
echo "==> Dat cron thuc thi het han"
CRON_LINE="*/10 * * * * cd $SITE_DIR && bash scripts/check-key-expiry.sh >> /tmp/${SITE_NAME:-aikiro}-expiry.log 2>&1"
# Xoa dong cu cua CHINH site nay roi them lai, de chay lai script khong nhan ban
# cron, va khong dung den cron cua site khac.
( crontab -l 2>/dev/null | grep -vF "cd $SITE_DIR && bash scripts/check-key-expiry.sh" || true
  echo "$CRON_LINE" ) | crontab -
echo "    moi 10 phut"
echo

# --- 6. Kiem tra -----------------------------------------------------------
echo "==> Kiem tra"
sleep 5
PORT="${KIRO_PORT:-8090}"

# Phep thu quan trong nhat: ban goc co dung admin API ma keyadmin can khong.
CODE="$(curl -s -o /dev/null -w '%{http_code}' \
  -H "X-Admin-Password: ${ADMIN_PASSWORD}" \
  "http://127.0.0.1:${PORT}/admin/api/api-keys" || echo 000)"
if [ "$CODE" = "200" ]; then
  echo "    [OK]   admin API ban goc tra 200 - keyadmin dung duoc"
else
  echo "    [LOI]  admin API tra $CODE - keyadmin/bot se khong hoat dong"
  echo "           kiem tra: docker logs ${SITE_NAME:-aikiro}-go --tail 30"
fi

for svc in keyadmin telegram-bot; do
  name="${SITE_NAME:-aikiro}-$svc"
  if [ "$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null)" = "true" ]; then
    echo "    [OK]   $svc dang chay"
  else
    echo "    [LOI]  $svc khong chay - docker logs $name --tail 30"
  fi
done

echo
echo "==> Xong. Ban goc @ $UPSTREAM_SHA, khong sua gi."
echo
echo "Buoc tiep theo:"
echo "  1. Them tai khoan Kiro (rieng cho site nay, khong dung chung site cu)"
echo "  2. Trong Telegram gui /help cho bot"
echo "  3. Nho nguoi khac chat voi bot -> phai KHONG co phan hoi"
