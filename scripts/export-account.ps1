<#
  export-account.ps1  --  Xuat thong tin tai khoan Kiro IDE ra text JSON chuan
                          de COPY roi DAN vao trang Import Account tren web.

  CACH DUNG (sau khi da dang nhap Kiro IDE):
      powershell -ExecutionPolicy Bypass -File .\scripts\export-account.ps1

  Script se:
    1. Doc file token Kiro IDE.
    2. Sinh JSON account chuan (ho tro external_idp / social / idc).
    3. In ra man hinh + TU DONG COPY vao clipboard.
  Sau do ban chi can mo trang Import Account tren web va dan (Ctrl+V) vao.
#>

param(
  [string]$Nickname = "",
  [string]$TokenPath = "$env:USERPROFILE\.aws\sso\cache\kiro-auth-token.json"
)

$ErrorActionPreference = "Stop"

if (-not (Test-Path $TokenPath)) {
  Write-Host "LOI: Khong tim thay token: $TokenPath" -ForegroundColor Red
  Write-Host "     Ban da dang nhap Kiro IDE chua?" -ForegroundColor Yellow
  exit 1
}

$t = Get-Content $TokenPath -Raw | ConvertFrom-Json
if (-not $t.refreshToken) {
  Write-Host "LOI: Token thieu refreshToken - dang nhap lai Kiro IDE." -ForegroundColor Red
  exit 1
}

# --- Suy ra authMethod + provider ---
$issuer = ("" + $t.issuerUrl).ToLower()
$provider = "" + $t.provider
$am = "" + $t.authMethod
if ($issuer -match "microsoftonline" -or $am -eq "external_idp") {
  $authMethod = "external_idp"
  if (-not $provider -or $provider -eq "ExternalIdp") { $provider = "AzureAD" }
} elseif ($provider -match "(?i)google|github") {
  $authMethod = "social"
} elseif ($am -in @("idc","builderId","iam")) {
  $authMethod = "idc"; if (-not $provider) { $provider = "BuilderId" }
} elseif ($issuer) {
  $authMethod = "external_idp"; if (-not $provider) { $provider = "AzureAD" }
} else {
  $authMethod = "social"; if (-not $provider) { $provider = "Kiro SSO" }
}

# --- Nickname: tu lay email tu JWT neu chua dat ---
if (-not $Nickname) {
  try {
    $parts = $t.accessToken.Split(".")
    $p = $parts[1].Replace("-","+").Replace("_","/")
    switch ($p.Length % 4) { 2 {$p+="=="} 3 {$p+="="} }
    $j = [System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($p)) | ConvertFrom-Json
    $Nickname = $j.email; if (-not $Nickname){$Nickname=$j.upn}; if (-not $Nickname){$Nickname=$j.preferred_username}
  } catch { }
  if (-not $Nickname) { $Nickname = "kiro-" + (Get-Date -Format "yyyyMMdd-HHmm") }
}

# --- expiresAt -> unix seconds ---
$exp = 0
try { $exp = [DateTimeOffset]::Parse($t.expiresAt).ToUnixTimeSeconds() } catch { }

# --- clientId + clientSecret cho idc/builderId ---
# Refresh idc/builderId qua endpoint OIDC cua AWS CAN ca clientId lan clientSecret.
# Token file chi luu 'clientIdHash' (khong phai secret). Credential that nam o file
# dang ky client rieng: <cache_dir>/<clientIdHash>.json. Doc file do de lay ra.
$clientId = "" + $t.clientId
$clientSecret = ""
if ($authMethod -in @("idc", "builderId") -and $t.clientIdHash) {
  $regPath = Join-Path (Split-Path $TokenPath -Parent) ("" + $t.clientIdHash + ".json")
  if (Test-Path $regPath) {
    try {
      $reg = Get-Content $regPath -Raw | ConvertFrom-Json
      if ($reg.clientId)     { $clientId = "" + $reg.clientId }
      if ($reg.clientSecret) { $clientSecret = "" + $reg.clientSecret }
    } catch { }
  }
  if (-not $clientSecret) {
    Write-Host "CANH BAO: khong lay duoc clientSecret ($regPath) - account idc se KHONG refresh duoc." -ForegroundColor Yellow
  }
}

# --- Dung account object (dung dung ten truong config.Account cua Kiro-Go) ---
$acct = [ordered]@{
  nickname     = $Nickname
  accessToken  = $t.accessToken
  refreshToken = $t.refreshToken
  clientId     = $clientId
  authMethod   = $authMethod
  provider     = $provider
  region       = if ($t.region) { $t.region } else { "us-east-1" }
  expiresAt    = $exp
  enabled      = $true
}
if ($authMethod -in @("idc", "builderId") -and $clientSecret) {
  $acct.clientSecret = $clientSecret
}
if ($authMethod -eq "external_idp") {
  $acct.tokenEndpoint = "" + $t.tokenEndpoint
  $acct.issuerUrl     = "" + $t.issuerUrl
  $acct.scopes        = "" + $t.scopes
}

$json = $acct | ConvertTo-Json -Compress

# --- Copy vao clipboard ---
try { Set-Clipboard -Value $json; $copied = $true } catch { $copied = $false }

Write-Host ""
Write-Host "===== TAI KHOAN: $Nickname ($authMethod / $provider) =====" -ForegroundColor Green
Write-Host ""
Write-Host $json
Write-Host ""
if ($copied) {
  Write-Host ">>> DA COPY vao clipboard. Mo trang Import Account va dan (Ctrl+V)." -ForegroundColor Cyan
} else {
  Write-Host ">>> Copy doan JSON tren, dan vao trang Import Account." -ForegroundColor Yellow
}
Write-Host "    Trang import: https://api.mmodiary.com/admin/import-account.html" -ForegroundColor Cyan
