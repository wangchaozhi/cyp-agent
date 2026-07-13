[CmdletBinding()]
param(
  [string]$BackendHost = "127.0.0.1",
  [int]$BackendPort = 8000,
  [string]$FrontendHost = "127.0.0.1",
  [int]$FrontendPort = 5173,
  [switch]$NoKill,
  [switch]$AllowMultipleBackends,
  [switch]$SkipInstall
)

$ErrorActionPreference = "Stop"
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$OutputEncoding = [System.Text.UTF8Encoding]::new($false)

$ProjectRoot = Split-Path -Parent $PSScriptRoot
$WebRoot = Join-Path $ProjectRoot "apps\web"
$LogDir = Join-Path $ProjectRoot ".logs"

function Write-Step {
  param([string]$Message)
  Write-Host "[cyp-agent] $Message"
}

function Get-RequiredCommand {
  param([string]$Name)
  $command = Get-Command $Name -ErrorAction SilentlyContinue
  if (-not $command) {
    throw "Cannot find $Name. Add it to PATH."
  }
  return $command.Source
}

function Test-LoopbackHost {
  param([string]$HostName)
  $normalized = $HostName.Trim().Trim('[', ']')
  if ($normalized -eq 'localhost') { return $true }
  $address = $null
  if ([System.Net.IPAddress]::TryParse($normalized, [ref]$address)) {
    return [System.Net.IPAddress]::IsLoopback($address)
  }
  return $false
}

function Stop-ProcessTree {
  param([int]$ProcessId)
  if (-not $ProcessId -or $ProcessId -eq $PID) { return }
  $process = Get-Process -Id $ProcessId -ErrorAction SilentlyContinue
  if ($process) {
    Write-Step "Stopping PID $ProcessId ($($process.ProcessName))"
    taskkill /PID $ProcessId /T /F | Out-Null
  }
}

function Stop-PortListeners {
  param([int[]]$Ports)
  $listeners = Get-NetTCPConnection -LocalPort $Ports -State Listen -ErrorAction SilentlyContinue
  $processIds = $listeners |
    Select-Object -ExpandProperty OwningProcess -Unique |
    Where-Object { $_ -and $_ -ne 0 -and $_ -ne $PID }
  foreach ($processId in $processIds) {
    Stop-ProcessTree -ProcessId ([int]$processId)
  }
  for ($attempt = 0; $attempt -lt 15; $attempt++) {
    $remaining = Get-NetTCPConnection -LocalPort $Ports -State Listen -ErrorAction SilentlyContinue
    if (-not $remaining) { return }
    Start-Sleep -Milliseconds 300
  }
  throw "Ports are still occupied: $($Ports -join ', ')"
}

function Start-LoggedProcess {
  param(
    [string]$FilePath,
    [string[]]$Arguments,
    [string]$WorkingDirectory,
    [string]$Stdout,
    [string]$Stderr
  )
  "" | Set-Content -LiteralPath $Stdout -Encoding utf8
  "" | Set-Content -LiteralPath $Stderr -Encoding utf8
  Start-Process `
    -FilePath $FilePath `
    -ArgumentList $Arguments `
    -WorkingDirectory $WorkingDirectory `
    -WindowStyle Hidden `
    -RedirectStandardOutput $Stdout `
    -RedirectStandardError $Stderr `
    -PassThru
}

function Wait-Http {
  param([string]$Name, [string]$Url, [string]$ErrorLog)
  for ($attempt = 0; $attempt -lt 40; $attempt++) {
    try {
      $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 2
      if ($response.StatusCode -ge 200 -and $response.StatusCode -lt 500) {
        Write-Step "$Name is ready: $Url"
        return
      }
    } catch {
      Start-Sleep -Milliseconds 500
    }
  }
  Get-Content -LiteralPath $ErrorLog -Tail 50 -ErrorAction SilentlyContinue
  throw "$Name did not become ready: $Url"
}

$GoCommand = Get-RequiredCommand -Name "go"
$NpmCommand = Get-RequiredCommand -Name "npm.cmd"
if ((-not (Test-LoopbackHost $BackendHost) -or -not (Test-LoopbackHost $FrontendHost)) -and
    [string]::IsNullOrWhiteSpace($env:CYP_API_TOKEN)) {
  throw "CYP_API_TOKEN is required when backend or frontend listens on a non-loopback host."
}
New-Item -ItemType Directory -Force -Path $LogDir | Out-Null

if (-not $NoKill) {
  Stop-PortListeners -Ports @($BackendPort, $FrontendPort)
}

if (-not $AllowMultipleBackends) {
  $listenerProcessIds = Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue |
    Select-Object -ExpandProperty OwningProcess -Unique
  $otherBackends = Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
    Where-Object {
      $listenerProcessIds -contains $_.ProcessId -and $_.ProcessId -ne $PID -and (
        $_.Name -like "cyp-server*.exe" -or
        $_.CommandLine -match '(?i)go(?:\.exe)?\s+run\s+\.\/cmd\/cyp-server'
      )
    }
  if ($otherBackends) {
    $details = ($otherBackends | ForEach-Object { "PID=$($_.ProcessId) $($_.Name)" }) -join ", "
    throw "Another cyp-agent backend is already running ($details). Stop it first or pass -AllowMultipleBackends explicitly. Multiple automation runtimes can duplicate analysis and orders."
  }
}

if (-not $SkipInstall -and -not (Test-Path -LiteralPath (Join-Path $WebRoot "node_modules"))) {
  Write-Step "Installing frontend dependencies with npm ci"
  Push-Location $WebRoot
  try {
    & $NpmCommand ci
    if ($LASTEXITCODE -ne 0) { throw "npm ci failed" }
  } finally {
    Pop-Location
  }
}

$backendOut = Join-Path $LogDir "backend.out.log"
$backendErr = Join-Path $LogDir "backend.err.log"
$frontendOut = Join-Path $LogDir "frontend.out.log"
$frontendErr = Join-Path $LogDir "frontend.err.log"

Write-Step "Starting Go backend"
$backend = Start-LoggedProcess `
  -FilePath $GoCommand `
  -Arguments @("run", "./cmd/cyp-server", "-host", $BackendHost, "-port", "$BackendPort") `
  -WorkingDirectory $ProjectRoot `
  -Stdout $backendOut `
  -Stderr $backendErr

$previousBackendUrl = $env:VITE_BACKEND_URL
$env:VITE_BACKEND_URL = "http://${BackendHost}:${BackendPort}"
try {
  Write-Step "Starting frontend (API proxy: $env:VITE_BACKEND_URL)"
  $frontend = Start-LoggedProcess `
    -FilePath $NpmCommand `
    -Arguments @("run", "dev", "--", "--host", $FrontendHost, "--port", "$FrontendPort", "--strictPort") `
    -WorkingDirectory $WebRoot `
    -Stdout $frontendOut `
    -Stderr $frontendErr
} finally {
  $env:VITE_BACKEND_URL = $previousBackendUrl
}

Wait-Http -Name "Backend" -Url "http://${BackendHost}:${BackendPort}/api/health" -ErrorLog $backendErr
Wait-Http -Name "Frontend" -Url "http://127.0.0.1:${FrontendPort}/" -ErrorLog $frontendErr

Write-Host ""
Write-Step "Started"
Write-Host "  Backend:  http://${BackendHost}:${BackendPort}"
Write-Host "  Frontend: http://127.0.0.1:${FrontendPort}"
Write-Host "  Backend launcher PID:  $($backend.Id)"
Write-Host "  Frontend launcher PID: $($frontend.Id)"
Write-Host "  Logs: $LogDir"
