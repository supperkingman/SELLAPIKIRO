#!/usr/bin/env bash
# update-admin-ip.sh - Doi/them IP duoc phep vao /admin (allowlist) mot cach de dang.
#
# Cach dung (chay tren VPS voi sudo):
#   sudo bash scripts/update-admin-ip.sh <lenh> [IP]
#
# Lenh:
#   show                  Xem danh sach IP dang duoc phep
#   add   <IP>            Them 1 IP vao allowlist
#   set   <IP>            THAY THE toan bo allowlist bang 1 IP (+ localhost)
#   del   <IP>            Bo 1 IP khoi allowlist
#   myip                 Them IP hien tai cua ban (tu dong lay qua Internet)
#
# Vi du:
#   sudo bash scripts/update-admin-ip.sh myip
#   sudo bash scripts/update-admin-ip.sh add 1.2.3.4
#   sudo bash scripts/update-admin-ip.sh show
#
# Sau khi doi, script tu backup + kiem tra + graceful restart OLS.
set -euo pipefail

# Duong dan vhost OLS - sua neu site khac.
VHOST="${VHOST:-/usr/local/lsws/conf/vhosts/site_144836/vhosts.conf}"
LSWSCTRL="${LSWSCTRL:-/usr/local/lsws/bin/lswsctrl}"

die() { echo "LOI: $*" >&2; exit 1; }

[ -f "$VHOST" ] || die "Khong tim thay vhost: $VHOST (dat bien VHOST=... neu khac)"

CMD="${1:-show}"
IP="${2:-}"

# Lay dong "allow" trong khoi context /admin.
current_allow() {
  awk '/context \/admin/{f=1} f&&/allow/{print;exit}' "$VHOST" | sed 's/^[[:space:]]*allow[[:space:]]*//'
}

show() {
  echo "=== IP dang duoc phep vao /admin ==="
  local line; line="$(current_allow || true)"
  if [ -z "$line" ]; then
    echo "(khong tim thay khoi context /admin hoac dong allow)"
  else
    echo "$line"
  fi
}

validate_ip() {
  echo "$1" | grep -qE '^[0-9]{1,3}(\.[0-9]{1,3}){3}$' || die "IP khong hop le: $1"
}

# Ghi lai dong allow moi (giu localhost 127.0.0.1 luon co mat).
write_allow() {
  local newlist="$1"
  cp "$VHOST" "$VHOST.bak-$(date +%Y%m%d-%H%M%S)"
  # Thay dong allow dau tien SAU dong "context /admin".
  awk -v newlist="$newlist" '
    /context \/admin/{f=1}
    f && /allow/ && !done {
      sub(/allow.*/, "allow                 " newlist); done=1
    }
    {print}
  ' "$VHOST" > "$VHOST.tmp" && mv "$VHOST.tmp" "$VHOST"

  # Kiem tra ngoac can bang truoc khi restart.
  local o c; o=$(grep -o '{' "$VHOST" | wc -l); c=$(grep -o '}' "$VHOST" | wc -l)
  [ "$o" = "$c" ] || die "Ngoac {} lech ($o/$c) - kiem tra lai $VHOST (co ban backup .bak-*)"

  echo "Danh sach moi: $newlist"
  "$LSWSCTRL" restart && echo "=== DA RESTART OLS ==="
}

case "$CMD" in
  show) show ;;
  add)
    [ -n "$IP" ] || die "Thieu IP. Vi du: add 1.2.3.4"
    validate_ip "$IP"
    cur="$(current_allow)"; [ -n "$cur" ] || cur="127.0.0.1"
    echo "$cur" | grep -qw "$IP" && { echo "IP $IP da co san."; exit 0; }
    write_allow "$cur, $IP"
    ;;
  set)
    [ -n "$IP" ] || die "Thieu IP. Vi du: set 1.2.3.4"
    validate_ip "$IP"
    write_allow "127.0.0.1, $IP"
    ;;
  del)
    [ -n "$IP" ] || die "Thieu IP. Vi du: del 1.2.3.4"
    cur="$(current_allow)"
    newlist="$(echo "$cur" | tr ',' '\n' | sed 's/^[[:space:]]*//' | grep -vw "$IP" | paste -sd ',' - | sed 's/,/, /g')"
    [ -n "$newlist" ] || newlist="127.0.0.1"
    write_allow "$newlist"
    ;;
  myip)
    myip="$(curl -fsS -m 10 https://api.ipify.org || curl -fsS -m 10 https://ifconfig.me)"
    validate_ip "$myip"
    echo "IP hien tai cua ban: $myip"
    cur="$(current_allow)"; [ -n "$cur" ] || cur="127.0.0.1"
    echo "$cur" | grep -qw "$myip" && { echo "IP $myip da co san."; exit 0; }
    write_allow "$cur, $myip"
    ;;
  *) die "Lenh khong hop le. Dung: show | add <IP> | set <IP> | del <IP> | myip" ;;
esac
