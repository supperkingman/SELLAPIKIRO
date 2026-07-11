#!/usr/bin/env bash
# Deploy hook cho FlashPanel GitHub auto-deploy.
# FlashPanel se chay file nay sau moi lan pull code tu GitHub.
# Trong FlashPanel: muc "Deploy Script" / "Post-deploy command", dien:
#     bash deploy-hook.sh
set -euo pipefail

cd "$(dirname "$0")"

echo "==> Deploy hook bat dau: $(date)"

# 1. Kiem tra .env (chua secret, KHONG nam trong git - tao thu cong 1 lan tren VPS)
if [ ! -f .env ]; then
  echo "!! Thieu file .env tren VPS. Tao tu .env.example va dat ADMIN_PASSWORD:"
  echo "     cp .env.example .env && nano .env"
  exit 1
fi

# 2. Tao thu muc du lieu (persist tai khoan + API key)
mkdir -p data

# 2b. Dam bao data/config.json ton tai duoi dang FILE truoc khi mount vao keycheck.
#     Neu chua co, Docker se tao nham 1 THU MUC cung ten -> hong. Tao file rong hop le.
if [ -d data/config.json ]; then
  echo "!! data/config.json dang la thu muc (do mount khi chua co file). Dang sua..."
  rmdir data/config.json 2>/dev/null || rm -rf data/config.json
fi
if [ ! -f data/config.json ]; then
  echo "==> Tao data/config.json rong (kiro-go se tu dien khi khoi dong)"
  # host=0.0.0.0 de kiro-go bind moi interface trong container (neu de rong,
  # mac dinh 127.0.0.1 -> Docker forward cong khong cham toi -> curl tra 000).
  printf '{"host":"0.0.0.0","requireApiKey":false,"apiKeys":[],"accounts":[]}' > data/config.json
fi

# 3. Build kiro-go tu source fork (zsecducna - ho tro external_idp) + sidecar keycheck.
#    Fork khong co image dung san nen build thay vi pull. --pull de lay base image moi.
echo "==> docker compose build (kiro-go tu fork + keycheck)"
docker compose build --pull

echo "==> docker compose up -d"
docker compose up -d
# Force-recreate rieng kiro-go: cac file JS custom mount theo tung-file bi ket
# inode cu sau khi git pull ghi de -> container khong thay noi dung moi. Recreate
# de container bind lai dung file hien tai (tinh nang moi luon co hieu luc sau deploy).
echo "==> Force-recreate kiro-go de nap file custom moi"
docker compose up -d --force-recreate --no-deps kiro-go

# 4. Chen the <script> tinh nang tao key hang loat vao index.html.
#    index.html nam trong image nen bi reset moi lan pull -> phai chen lai moi deploy.
#    Idempotent: chi chen neu chua co.
# 4. Tinh nang tao key hang loat gio duoc entrypoint.sh tu dong chen moi lan
#    container khoi dong (xem web-custom/entrypoint.sh + docker-compose.yml).
#    Khong can chen thu cong o day nua -> ben vung du container bi recreate.

# 5. Don dep image cu
docker image prune -f >/dev/null 2>&1 || true

echo "==> Trang thai:"
docker compose ps

echo "==> Deploy hook hoan tat: $(date)"
