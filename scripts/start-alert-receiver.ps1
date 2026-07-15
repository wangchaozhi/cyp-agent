[CmdletBinding()]
param(
  [int]$Port = 8081,
  [string]$DataFile = "data/alerts.jsonl"
)

$ErrorActionPreference = "Stop"
$ProjectRoot = Split-Path -Parent $PSScriptRoot
$LogDir = Join-Path $ProjectRoot ".logs"
New-Item -ItemType Directory -Force -Path $LogDir | Out-Null

$listeners = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue
foreach ($processId in ($listeners | Select-Object -ExpandProperty OwningProcess -Unique)) {
  if ($processId -and $processId -ne $PID) {
    taskkill /PID $processId /T /F | Out-Null
  }
}

$go = (Get-Command go -ErrorAction Stop).Source
$stdout = Join-Path $LogDir "alert-receiver.out.log"
$stderr = Join-Path $LogDir "alert-receiver.err.log"
"" | Set-Content -LiteralPath $stdout -Encoding utf8
"" | Set-Content -LiteralPath $stderr -Encoding utf8
$process = Start-Process -FilePath $go `
  -ArgumentList @("run", "./cmd/cyp-alert-receiver", "-port", "$Port", "-data-file", $DataFile) `
  -WorkingDirectory $ProjectRoot -WindowStyle Hidden `
  -RedirectStandardOutput $stdout -RedirectStandardError $stderr -PassThru

for ($attempt = 0; $attempt -lt 30; $attempt++) {
  try {
    $response = Invoke-WebRequest -Uri "http://127.0.0.1:$Port/health" -UseBasicParsing -TimeoutSec 2
    if ($response.StatusCode -eq 200) {
      Write-Host "Alert receiver: http://127.0.0.1:$Port"
      Write-Host "Webhook:        http://127.0.0.1:$Port/webhook/alerts"
      Write-Host "Alerts:         http://127.0.0.1:$Port/alerts"
      exit 0
    }
  } catch {
    Start-Sleep -Milliseconds 500
  }
}
Get-Content -LiteralPath $stderr -Tail 50 -ErrorAction SilentlyContinue
throw "Alert receiver did not become ready (launcher PID $($process.Id))."
