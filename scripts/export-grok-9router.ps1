# export-grok-9router.ps1
# Xuat tai khoan Grok CLI (Grok Build) tu 9router local de dan vao Kiro-Go.
#
# Nguon: %AppData%\Roaming\9router\db\data.sqlite
# Bang: providerConnections (JSON), loc provider = grok-cli
#
# Output: 1 file .txt (JSON array) de paste vao /admin/import-grok.html
#         hoac POST /admin/api/grok-accounts

param(
  [string]$OutFile = "",
  [string]$DbPath = ""
)

$ErrorActionPreference = "Stop"

if (-not $DbPath) {
  $DbPath = Join-Path $env:APPDATA "9router\db\data.sqlite"
}
if (-not (Test-Path $DbPath)) {
  Write-Error "Khong tim thay 9router DB: $DbPath"
}

if (-not $OutFile) {
  $stamp = Get-Date -Format "yyyyMMdd-HHmmss"
  $OutFile = Join-Path (Get-Location) "grok-accounts-$stamp.txt"
}

# Prefer python (sqlite3) - Windows often lacks sqlite3 CLI
$py = Get-Command python -ErrorAction SilentlyContinue
if (-not $py) { $py = Get-Command py -ErrorAction SilentlyContinue }
if (-not $py) {
  Write-Error "Can Python de doc SQLite (cai python.org). Khong dung sqlite3 CLI."
}

$script = @"
import json, sqlite3, sys, time
from datetime import datetime

db = sys.argv[1]
out = sys.argv[2]
conn = sqlite3.connect(db)
cur = conn.cursor()
# table may be provider_connections or providerConnections depending on version
tables = [r[0] for r in cur.execute("SELECT name FROM sqlite_master WHERE type='table'").fetchall()]
table = None
for t in tables:
    if t.lower() in ('providerconnections', 'provider_connections'):
        table = t
        break
if not table:
    print('Tables:', tables, file=sys.stderr)
    raise SystemExit('providerConnections table not found')

rows = cur.execute(f'SELECT * FROM "{table}"').fetchall()
cols = [d[0] for d in cur.description]
accounts = []
for row in rows:
    obj = dict(zip(cols, row))
    # value may be JSON blob in one column
    payload = None
    if 'data' in obj and obj['data']:
        try:
            payload = json.loads(obj['data']) if isinstance(obj['data'], str) else obj['data']
        except Exception:
            payload = None
    if payload is None:
        # whole row might already be flat-ish; try json columns
        payload = {}
        for k,v in obj.items():
            if isinstance(v, str) and v.startswith('{'):
                try:
                    payload.update(json.loads(v))
                except Exception:
                    pass
            else:
                payload[k] = v

    provider = (payload.get('provider') or obj.get('provider') or '').lower()
    if provider not in ('grok-cli', 'grok_cli', 'grokbuild', 'grok-build'):
        # also accept nested
        if 'grok' not in provider:
            continue

    data = payload.get('data') if isinstance(payload.get('data'), dict) else payload
    ps = data.get('providerSpecificData') or payload.get('providerSpecificData') or {}
    exp = data.get('expiresAt') or payload.get('expiresAt')
    expires_at = 0
    if isinstance(exp, (int, float)):
        expires_at = int(exp // 1000 if exp > 1e12 else exp)
    elif isinstance(exp, str) and exp:
        try:
            # ISO
            from datetime import datetime, timezone
            expires_at = int(datetime.fromisoformat(exp.replace('Z','+00:00')).timestamp())
        except Exception:
            try:
                expires_at = int(exp)
            except Exception:
                expires_at = 0

    acc = {
        'id': payload.get('id') or data.get('id') or obj.get('id'),
        'provider': 'grok-cli',
        'email': ps.get('email') or data.get('email') or payload.get('name') or '',
        'nickname': payload.get('name') or data.get('displayName') or '',
        'displayName': data.get('displayName') or ps.get('email') or '',
        'accessToken': data.get('accessToken') or '',
        'refreshToken': data.get('refreshToken') or '',
        'expiresAt': expires_at,
        'scope': data.get('scope') or '',
        'clientId': data.get('clientId') or 'b1a00492-073a-47ea-816f-4c329264a828',
        'authMethod': ps.get('authMethod') or data.get('authMethod') or 'device_code',
        'userId': ps.get('userId') or '',
        'idToken': ps.get('idToken') or '',
        'enabled': True if payload.get('enabled', True) not in (0, False, '0', 'false') else False,
    }
    if not acc['accessToken'] and not acc['refreshToken']:
        continue
    accounts.append(acc)

with open(out, 'w', encoding='utf-8') as f:
    json.dump(accounts, f, ensure_ascii=False, indent=2)
print(f'Exported {len(accounts)} Grok account(s) -> {out}')
"@

$tmpPy = Join-Path $env:TEMP "export_grok_9router.py"
Set-Content -Path $tmpPy -Value $script -Encoding UTF8
& $py.Source $tmpPy $DbPath $OutFile
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Write-Host ""
Write-Host "Buoc tiep:"
Write-Host "  1. Mo Kiro-Go admin -> Import Grok  ( /admin/import-grok.html )"
Write-Host "  2. Dan noi dung file: $OutFile"
Write-Host "  3. Khach goi model: grok-4.5 / grok-4.5-high|medium|low"
Write-Host "  Credit: 1 credit = 1000 tokens (input+output), tru tren API key khach."
