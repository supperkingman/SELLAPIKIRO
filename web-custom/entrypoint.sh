#!/bin/sh
# entrypoint.sh - Wrapper khoi dong kiro-go, tu dong chen tinh nang tao key hang loat.
#
# Chay MOI LAN container khoi dong -> tinh nang khong bao gio bi mat du container
# co bi recreate/restart (index.html nam trong image nen bi reset moi lan).
#
# Duoc mount vao container va dat lam entrypoint qua docker-compose.yml.
set -e

INDEX="/app/web/index.html"
# Bump this when custom JS changes so browsers always fetch the new file.
# entrypoint rewrites every matching <script src="/admin/NAME?v=..."> on each boot.
CUSTOM_JS_VERSION="6"

# Danh sach cac file JS custom can chen vao index.html (self-healing).
# Them file moi chi can them ten vao day.
CUSTOM_JS="custom-bulk-keys.js custom-import-account.js custom-import-grok.js custom-grok-accounts.js custom-import-codex.js custom-codex-accounts.js custom-key-expiry-display.js custom-key-controls.js custom-key-dashboard.js"

if [ ! -f "$INDEX" ]; then
  echo "[entrypoint] Khong tim thay index.html - bo qua chen custom JS."
elif ! grep -q "</body>" "$INDEX" 2>/dev/null; then
  echo "[entrypoint] !! index.html khong co </body> - bo qua chen custom JS."
else
  # Moi the <script> duoc chen tren MOT DONG RIENG, ngay TRUOC dong </body>.
  #
  # Truoc day cac the duoc chen cung dong voi </body> ("s#</body>#tag</body>#"),
  # nen den lan khoi dong sau, buoc don dep "sed /$js/d" xoa CA DONG do -> mat
  # luon </body> va TAT CA the script khac. Sau do khong con </body> de chen lai,
  # nen moi tinh nang custom bien mat sau lan restart thu hai.
  #
  # Cach lam hien tai an toan voi restart nhieu lan:
  #   - Xoa chi nhung dong VUA chua ten file VUA la the script (khong bao gio
  #     trung dong </body>, vi the va </body> luon o hai dong khac nhau).
  #   - Chen the moi thanh mot dong rieng truoc </body> (bang awk).
  for js in $CUSTOM_JS; do
    if [ ! -f "/app/web/$js" ]; then
      echo "[entrypoint] Bo qua $js (khong duoc mount)."
      continue
    fi
    tag="<script src=\"/admin/${js}?v=${CUSTOM_JS_VERSION}\" defer></script>"

    existed=0
    if grep -q "$js" "$INDEX" 2>/dev/null; then
      existed=1
      # Chi xoa dong la the script nap dung file nay. Dieu kien "/<script/"
      # dam bao khong bao gio xoa dong chua </body>.
      sed -i "\#<script[^>]*${js}#d" "$INDEX"
    fi

    # Chen thanh dong rieng ngay truoc dong dau tien co </body>.
    if grep -q "</body>" "$INDEX" 2>/dev/null; then
      awk -v tag="$tag" '
        !done && index($0, "</body>") { print tag; done = 1 }
        { print }
      ' "$INDEX" > "${INDEX}.tmp" && mv "${INDEX}.tmp" "$INDEX"
      if [ "$existed" = "1" ]; then
        echo "[entrypoint] Cap nhat $js -> ?v=${CUSTOM_JS_VERSION}"
      else
        echo "[entrypoint] Da chen $js vao UI (?v=${CUSTOM_JS_VERSION})."
      fi
    else
      echo "[entrypoint] !! Mat </body> trong index.html - khong chen duoc $js."
    fi
  done

  # Canh bao som neu so the script khong khop so file can chen.
  want=0
  for js in $CUSTOM_JS; do
    [ -f "/app/web/$js" ] && want=$((want + 1))
  done
  have=$(grep -c "/admin/custom-" "$INDEX" 2>/dev/null || echo 0)
  echo "[entrypoint] Custom JS da chen: ${have}/${want}"
fi

# Chuyen quyen dieu khien cho kiro-go (WORKDIR=/app, binary o /app/kiro-go).
echo "[entrypoint] Khoi dong kiro-go..."
exec ./kiro-go
