<#
  add-account.ps1  --  Them tai khoan Kiro vao he thong api.mmodiary.com chi bang 1 lenh.

  CACH DUNG (tren may Windows, sau khi da dang nhap Kiro IDE):
      powershell -ExecutionPolicy Bypass -File .\add-account.ps1
      # hoac dat nickname:
      powershell -ExecutionPolicy Bypass -File .\add-account.ps1 -Nickname "entra-2"

  Script se:
    1. Doc file token Kiro IDE tren may ban.
    2. Upload token len VPS (thu muc /tmp).
    3. Chay converter tren VPS de nap account vao data/config.json (can mat khau sudo).
    4. Xoa token tam + restart kiro-go.

  LUU Y: se hoi mat khau SSH 2 lan (1 cho upload, 1 cho chay lenh). Muon khoi hoi
  mat khau, xem phan "Thiet lap SSH key" o cuoi file README-ADD-ACCOUNT.md.
#>

param(
  [string]$Nickname = "",
  [string]$Vps       = "13.212.212.240",
  [string]$User      = "flashpanel",
  [string]$RemoteDir = "/home/flashpanel/api.mmodiary.com",
  [string]$TokenPath = "$env:USERPROFILE\.aws\sso\cache\kiro-auth-token.json"
)

$ErrorActionPreference = "Stop"
function Info($m){ Write-Host "==> $m" -ForegroundColor Cyan }
function Ok($m){ Write-Host "OK  $m" -ForegroundColor Green }
function Err($m){ Write-Host "LOI $m" -ForegroundColor Red }

# --- 1. Kiem tra file token ---
if (-not (Test-Path $TokenPath)) {
  Err "Khong tim thay file token: $TokenPath"
  Write-Host "   Ban da dang nhap Kiro IDE chua? Dang nhap xong roi chay lai script." -ForegroundColor Yellow
  exit 1
}

try { $tok = Get-Content $TokenPath -Raw | ConvertFrom-Json } catch {
  Err "File token khong phai JSON hop le."; exit 1
}
if (-not $tok.refreshToken) {
  Err "Token thieu refreshToken - khong dung duoc. Dang nhap lai Kiro IDE."; exit 1
}

# --- 2. Tu suy ra nickname neu chua dat (thu lay email tu JWT accessToken) ---
if (-not $Nickname) {
  $email = ""
  try {
    $parts = $tok.accessToken.Split(".")
    if ($parts.Length -ge 2) {
      $p = $parts[1].Replace("-","+").Replace("_","/")
      switch ($p.Length % 4) { 2 {$p+="=="} 3 {$p+="="} }
      $json = [System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($p)) | ConvertFrom-Json
      $email = $json.email; if (-not $email){ $email = $json.upn }; if (-not $email){ $email = $json.preferred_username }
    }
  } catch { }
  if ($email) { $Nickname = $email } else { $Nickname = "kiro-" + (Get-Date -Format "yyyyMMdd-HHmm") }
}

Info "Tai khoan se them: $Nickname"
Info "May chu dich   : $User@$Vps"
Write-Host ""

# --- 3. Upload token len /tmp cua VPS (khong can sudo cho buoc nay) ---
$remoteTmp = "/tmp/kiro-token-upload.json"
Info "Buoc 1/2: Upload token (nhap mat khau SSH khi duoc hoi)..."
& scp $TokenPath "${User}@${Vps}:${remoteTmp}"
if ($LASTEXITCODE -ne 0) { Err "Upload that bai."; exit 1 }
Ok "Da upload token."
Write-Host ""

# --- 4. Chay converter + restart tren VPS (can sudo -> dung ssh -t) ---
$remoteCmd = "cd '$RemoteDir' && " +
             "sudo python3 scripts/kiro_token_to_account.py '$remoteTmp' data/config.json '$Nickname' && " +
             "rm -f '$remoteTmp' && " +
             "docker compose restart kiro-go && " +
             "echo '=== HOAN TAT ==='"

Info "Buoc 2/2: Nap account + restart (nhap mat khau SSH, roi mat khau sudo neu duoc hoi)..."
& ssh -t "${User}@${Vps}" $remoteCmd
if ($LASTEXITCODE -ne 0) { Err "Nap account that bai. Kiem tra thong bao ben tren."; exit 1 }

Write-Host ""
Ok "Da them tai khoan '$Nickname' vao he thong."
Write-Host "   Kiem tra: https://api.mmodiary.com/admin -> tab Accounts" -ForegroundColor Yellow
