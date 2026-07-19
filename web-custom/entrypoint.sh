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
CUSTOM_JS_VERSION="5"

# Danh sach cac file JS custom can chen vao index.html (self-healing).
# Them file moi chi can them ten vao day.
CUSTOM_JS="custom-bulk-keys.js custom-import-account.js custom-import-grok.js custom-grok-accounts.js custom-import-codex.js custom-codex-accounts.js custom-key-expiry-display.js custom-key-controls.js custom-key-dashboard.js"

if [ ! -f "$INDEX" ]; then
  echo "[entrypoint] Khong tim thay index.html - bo qua chen custom JS."
elif ! grep -q "</body>" "$INDEX" 2>/dev/null; then
  echo "[entrypoint] !! index.html khong co </body> - bo qua chen custom JS."
else
  for js in $CUSTOM_JS; do
    if [ ! -f "/app/web/$js" ]; then
      echo "[entrypoint] Bo qua $js (khong duoc mount)."
      continue
    fi
    tag="<script src=\"/admin/${js}?v=${CUSTOM_JS_VERSION}\" defer></script>"
    # Already present with any ?v=... â†’ force rewrite to current version (fixes stale cache).
    if grep -q "$js" "$INDEX" 2>/dev/null; then
      # Remove any previous script tags that load this file, then re-insert before </body>.
      # portable sed: delete lines containing the js filename as admin script src
      sed -i "/${js}/d" "$INDEX"
      sed -i "s#</body>#${tag}</body>#" "$INDEX"
      echo "[entrypoint] Cap nhat $js -> ?v=${CUSTOM_JS_VERSION}"
    else
      sed -i "s#</body>#${tag}</body>#" "$INDEX"
      echo "[entrypoint] Da chen $js vao UI (?v=${CUSTOM_JS_VERSION})."
    fi
  done
fi

# Chuyen quyen dieu khien cho kiro-go (WORKDIR=/app, binary o /app/kiro-go).
echo "[entrypoint] Khoi dong kiro-go..."
exec ./kiro-go
