<#
  Tao API key hang loat cho Kiro-Go qua admin API.
  Endpoint: POST /admin/api/api-keys  (header X-Admin-Password)

  VI DU:
    # Tao 10 key tren demo local (cong 8090)
    .\create-keys.ps1 -Count 10 -Password "demo_local_12345" -BaseUrl "http://127.0.0.1:8090" -NamePrefix "khach"

    # Tao 10 key tren production
    .\create-keys.ps1 -Count 10 -Password "<ADMIN_PASSWORD>" -BaseUrl "https://api.mmodiary.com" -NamePrefix "ban-le" -TokenLimit 1000000

  Key sinh ra duoc luu vao file CSV (mac dinh keys_output.csv) - phan phoi cho khach tu file nay.
#>
param(
  [int]$Count = 10,
  [Parameter(Mandatory = $true)][string]$Password,
  [string]$BaseUrl = "http://127.0.0.1:8090",
  [string]$NamePrefix = "key",
  [long]$TokenLimit = 0,      # 0 = khong gioi han token
  [double]$CreditLimit = 0,   # 0 = khong gioi han credit
  [string]$OutFile = "keys_output.csv"
)

$ErrorActionPreference = "Stop"
$endpoint = "$BaseUrl/admin/api/api-keys"
$headers = @{ "X-Admin-Password" = $Password; "Content-Type" = "application/json" }
$results = New-Object System.Collections.Generic.List[object]

Write-Host "Tao $Count key tai $endpoint ..." -ForegroundColor Cyan

for ($i = 1; $i -le $Count; $i++) {
  $name = "{0}-{1:yyyyMMdd}-{2:D3}" -f $NamePrefix, (Get-Date), $i
  $body = @{
    name        = $name
    enabled     = $true
    tokenLimit  = $TokenLimit
    creditLimit = $CreditLimit
    # khong gui "key" => server tu sinh key ngau nhien
  } | ConvertTo-Json -Compress

  try {
    $resp = Invoke-RestMethod -Uri $endpoint -Method Post -Headers $headers -Body $body -TimeoutSec 30
    $plain = $resp.key
    if (-not $plain) { $plain = "(server khong tra ve key - kiem tra phien ban)" }
    $results.Add([pscustomobject]@{ index = $i; name = $name; key = $plain })
    Write-Host ("[{0}/{1}] OK  {2}  ->  {3}" -f $i, $Count, $name, $plain) -ForegroundColor Green
  }
  catch {
    $msg = $_.Exception.Message
    Write-Host ("[{0}/{1}] LOI {2}: {3}" -f $i, $Count, $name, $msg) -ForegroundColor Red
    $results.Add([pscustomobject]@{ index = $i; name = $name; key = "ERROR: $msg" })
  }
}

$results | Export-Csv -Path $OutFile -NoTypeInformation -Encoding UTF8
Write-Host ""
Write-Host "Da luu $($results.Count) dong vao $OutFile" -ForegroundColor Cyan
