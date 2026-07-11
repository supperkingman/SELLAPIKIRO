#!/usr/bin/env bash
# manage-keys.sh - Bao cao & quan ly API key qua admin API cua Kiro-Go.
#
# Dung tren VPS (hoac may co the goi toi admin API). Doc mat khau admin tu .env
# hoac bien moi truong ADMIN_PASSWORD. KHONG in full key (admin API da mask san).
#
# Cach dung:
#   ./manage-keys.sh report                 # bang tom tat tat ca key + usage
#   ./manage-keys.sh report --csv out.csv   # xuat CSV
#   ./manage-keys.sh near-limit [PERCENT]   # key da dung >= PERCENT% (mac dinh 80)
#   ./manage-keys.sh unused                 # key chua co request nao
#   ./manage-keys.sh exhausted              # key da het han muc (token/credit)
#
# Bien moi truong:
#   BASE_URL (mac dinh http://127.0.0.1:8080)
#   ADMIN_PASSWORD (neu khong dat, doc tu .env)
set -euo pipefail
cd "$(dirname "$0")/.."

BASE_URL="${BASE_URL:-http://127.0.0.1:8080}"

# Lay mat khau admin
if [ -z "${ADMIN_PASSWORD:-}" ]; then
  if [ -f .env ]; then
    ADMIN_PASSWORD="$(grep -E '^ADMIN_PASSWORD=' .env | head -1 | cut -d= -f2-)"
  fi
fi
if [ -z "${ADMIN_PASSWORD:-}" ]; then
  echo "LOI: chua co ADMIN_PASSWORD (dat bien moi truong hoac trong .env)"
  exit 1
fi

if ! command -v python3 >/dev/null 2>&1; then
  echo "LOI: can python3 de xu ly JSON"
  exit 1
fi

CMD="${1:-report}"
ARG2="${2:-}"
ARG3="${3:-}"

# Lay danh sach key (JSON) tu admin API
fetch_keys() {
  curl -fsS -H "X-Admin-Password: $ADMIN_PASSWORD" "$BASE_URL/admin/api/api-keys"
}

JSON="$(fetch_keys)"

case "$CMD" in
  report)
    OUTCSV=""
    if [ "$ARG2" = "--csv" ]; then OUTCSV="$ARG3"; fi
    echo "$JSON" | OUTCSV="$OUTCSV" python3 - <<'PY'
import json, os, sys
data = json.load(sys.stdin)
keys = data.get("apiKeys", data if isinstance(data, list) else [])
rows = []
for k in keys:
    name = k.get("name") or "(khong ten)"
    tl, tu = k.get("tokenLimit",0), k.get("tokensUsed",0)
    cl, cu = k.get("creditLimit",0), k.get("creditsUsed",0)
    tpct = f"{(tu/tl*100):.0f}%" if tl>0 else "-"
    cpct = f"{(cu/cl*100):.0f}%" if cl>0 else "-"
    rows.append({
        "name": name, "enabled": k.get("enabled",False),
        "masked": k.get("keyMasked",""), "requests": k.get("requestsCount",0),
        "tokensUsed": tu, "tokenLimit": tl, "tokenPct": tpct,
        "creditsUsed": cu, "creditLimit": cl, "creditPct": cpct,
    })
outcsv = os.environ.get("OUTCSV","")
if outcsv:
    import csv
    with open(outcsv,"w",newline="",encoding="utf-8") as f:
        w = csv.DictWriter(f, fieldnames=list(rows[0].keys()) if rows else ["name"])
        w.writeheader(); w.writerows(rows)
    print(f"Da xuat {len(rows)} dong vao {outcsv}")
else:
    print(f"{'TEN':<26}{'BAT':<5}{'REQ':>7}  {'TOKEN':>22}  {'CREDIT':>18}")
    print("-"*84)
    for r in rows:
        tok = f"{r['tokensUsed']}/{r['tokenLimit'] or '∞'} ({r['tokenPct']})"
        cre = f"{r['creditsUsed']}/{r['creditLimit'] or '∞'} ({r['creditPct']})"
        en = "x" if r["enabled"] else "."
        print(f"{r['name'][:25]:<26}{en:<5}{r['requests']:>7}  {tok:>22}  {cre:>18}")
    print(f"\nTong: {len(rows)} key")
PY
    ;;

  near-limit)
    PCT="${ARG2:-80}"
    echo "$JSON" | PCT="$PCT" python3 - <<'PY'
import json, os, sys
pct = float(os.environ.get("PCT","80"))
data = json.load(sys.stdin)
keys = data.get("apiKeys", data if isinstance(data, list) else [])
hit = []
for k in keys:
    tl, tu = k.get("tokenLimit",0), k.get("tokensUsed",0)
    cl, cu = k.get("creditLimit",0), k.get("creditsUsed",0)
    tp = (tu/tl*100) if tl>0 else 0
    cp = (cu/cl*100) if cl>0 else 0
    if tp>=pct or cp>=pct:
        hit.append((k.get("name") or "(khong ten)", f"token {tp:.0f}%", f"credit {cp:.0f}%"))
print(f"Key da dung >= {pct:.0f}% han muc: {len(hit)}")
for n,t,c in hit: print(f"  - {n}: {t}, {c}")
PY
    ;;

  unused)
    echo "$JSON" | python3 - <<'PY'
import json, sys
data = json.load(sys.stdin)
keys = data.get("apiKeys", data if isinstance(data, list) else [])
un = [k.get("name") or "(khong ten)" for k in keys if (k.get("requestsCount",0)==0)]
print(f"Key chua dung (0 request): {len(un)}")
for n in un: print(f"  - {n}")
PY
    ;;

  exhausted)
    echo "$JSON" | python3 - <<'PY'
import json, sys
data = json.load(sys.stdin)
keys = data.get("apiKeys", data if isinstance(data, list) else [])
ex = []
for k in keys:
    tl, tu = k.get("tokenLimit",0), k.get("tokensUsed",0)
    cl, cu = k.get("creditLimit",0), k.get("creditsUsed",0)
    if (tl>0 and tu>=tl) or (cl>0 and cu>=cl):
        ex.append(k.get("name") or "(khong ten)")
print(f"Key da het han muc: {len(ex)}")
for n in ex: print(f"  - {n}")
PY
    ;;

  *)
    echo "Lenh khong hop le. Dung: report | near-limit [%] | unused | exhausted"
    exit 1
    ;;
esac
