[CmdletBinding()]
param(
  [string]$CondaEnv = "cyp-agent",
  [string]$BackendHost = "127.0.0.1",
  [int]$BackendPort = 8000,
  [string]$FrontendHost = "0.0.0.0",
  [int]$FrontendPort = 5173,
  [switch]$Reload,
  [switch]$NoKill,
  [switch]$SkipInstall
)

$ErrorActionPreference = "Stop"

try {
  [Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
  $OutputEncoding = [System.Text.UTF8Encoding]::new($false)
} catch {
  # Older PowerShell hosts can ignore UTF-8 setup.
}

$ProjectRoot = Split-Path -Parent $PSScriptRoot
$WebRoot = Join-Path $ProjectRoot "apps\web"
$LogDir = Join-Path $ProjectRoot ".logs"

function Write-Step {
  param([string]$Message)
  Write-Host "[cyp-agent] $Message"
}

function Get-CondaCommand {
  $command = Get-Command conda -ErrorAction SilentlyContinue
  if ($command) {
    return $command.Source
  }

  $fallbacks = @(
    "E:\Program Files\anaconda3\condabin\conda.bat",
    "$env:USERPROFILE\anaconda3\condabin\conda.bat",
    "$env:USERPROFILE\miniconda3\condabin\conda.bat"
  )

  foreach ($path in $fallbacks) {
    if ($path -and (Test-Path -LiteralPath $path)) {
      return $path
    }
  }

  throw "Cannot find conda. Add conda to PATH or edit scripts\start-dev.ps1."
}

function Stop-ProcessTree {
  param([int]$ProcessId)

  if (-not $ProcessId -or $ProcessId -eq 0 -or $ProcessId -eq $PID) {
    return
  }

  $children = Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
    Where-Object { $_.ParentProcessId -eq $ProcessId }

  foreach ($child in $children) {
    Stop-ProcessTree -ProcessId ([int]$child.ProcessId)
  }

  $proc = Get-Process -Id $ProcessId -ErrorAction SilentlyContinue
  if ($proc) {
    Write-Step "Stopping PID $ProcessId ($($proc.ProcessName))"
    taskkill /PID $ProcessId /T /F | Out-Null
  } else {
    Write-Step "PID $ProcessId is not visible; checking orphaned children"
  }

  $orphanChildren = Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
    Where-Object {
      $_.ParentProcessId -eq $ProcessId -or
      ($_.CommandLine -and $_.CommandLine -match "parent_pid=$ProcessId")
    }

  foreach ($child in $orphanChildren) {
    if ($child.ProcessId -ne $PID) {
      Write-Step "Stopping orphan child PID $($child.ProcessId)"
      taskkill /PID $child.ProcessId /T /F | Out-Null
    }
  }
}

function Stop-PortListeners {
  param([int[]]$Ports)

  $listeners = Get-NetTCPConnection -LocalPort $Ports -State Listen -ErrorAction SilentlyContinue
  $processIds = $listeners |
    Select-Object -ExpandProperty OwningProcess -Unique |
    Where-Object { $_ -and $_ -ne 0 }

  if (-not $processIds) {
    Write-Step "Ports $($Ports -join ', ') are free"
    return
  }

  foreach ($processId in $processIds) {
    Stop-ProcessTree -ProcessId ([int]$processId)
  }

  for ($i = 0; $i -lt 15; $i++) {
    Start-Sleep -Seconds 1
    $remaining = Get-NetTCPConnection -LocalPort $Ports -State Listen -ErrorAction SilentlyContinue
    if (-not $remaining) {
      Write-Step "Ports $($Ports -join ', ') are free"
      return
    }

    $remainingIds = $remaining |
      Select-Object -ExpandProperty OwningProcess -Unique |
      Where-Object { $_ -and $_ -ne 0 }

    foreach ($processId in $remainingIds) {
      Stop-ProcessTree -ProcessId ([int]$processId)
    }
  }

  $blocked = Get-NetTCPConnection -LocalPort $Ports -State Listen -ErrorAction SilentlyContinue |
    Select-Object LocalAddress, LocalPort, OwningProcess
  throw "Ports are still occupied: $($blocked | Out-String)"
}

function Invoke-Conda {
  param(
    [string]$CondaCommand,
    [string[]]$Arguments,
    [string]$WorkingDirectory
  )

  Push-Location $WorkingDirectory
  try {
    & $CondaCommand @Arguments
    if ($LASTEXITCODE -ne 0) {
      throw "Command failed: conda $($Arguments -join ' ')"
    }
  } finally {
    Pop-Location
  }
}

function Start-LoggedProcess {
  param(
    [string]$Name,
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
  param(
    [string]$Name,
    [string]$Url,
    [string]$ErrorLog
  )

  for ($i = 0; $i -lt 30; $i++) {
    try {
      $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 2
      if ($response.StatusCode -ge 200 -and $response.StatusCode -lt 500) {
        Write-Step "$Name is ready: $Url"
        return
      }
    } catch {
      Start-Sleep -Seconds 1
    }
  }

  Write-Host ""
  Write-Host "Last $Name log lines:"
  Get-Content -LiteralPath $ErrorLog -Tail 40 -ErrorAction SilentlyContinue
  throw "$Name did not become ready: $Url"
}

New-Item -ItemType Directory -Force -Path $LogDir | Out-Null

$CondaCommand = Get-CondaCommand
$backendOut = Join-Path $LogDir "backend.out.log"
$backendErr = Join-Path $LogDir "backend.err.log"
$frontendOut = Join-Path $LogDir "frontend.out.log"
$frontendErr = Join-Path $LogDir "frontend.err.log"

Write-Step "Project root: $ProjectRoot"
Write-Step "Conda env: $CondaEnv"

if (-not $NoKill) {
  Stop-PortListeners -Ports @($BackendPort, $FrontendPort)
}

if (-not $SkipInstall -and -not (Test-Path -LiteralPath (Join-Path $WebRoot "node_modules"))) {
  Write-Step "apps/web/node_modules is missing; running npm ci"
  Invoke-Conda `
    -CondaCommand $CondaCommand `
    -Arguments @("run", "-n", $CondaEnv, "npm", "ci") `
    -WorkingDirectory $WebRoot
}

$backendArgs = @(
  "run", "-n", $CondaEnv, "--no-capture-output",
  "python", "-m", "uvicorn", "apps.server.main:app",
  "--host", $BackendHost,
  "--port", "$BackendPort"
)

if ($Reload) {
  $backendArgs += "--reload"
}

$frontendArgs = @(
  "run", "-n", $CondaEnv, "--no-capture-output",
  "npm", "run", "dev", "--",
  "--host", $FrontendHost,
  "--port", "$FrontendPort",
  "--strictPort"
)

Write-Step "Starting backend"
$backend = Start-LoggedProcess `
  -Name "backend" `
  -FilePath $CondaCommand `
  -Arguments $backendArgs `
  -WorkingDirectory $ProjectRoot `
  -Stdout $backendOut `
  -Stderr $backendErr

Write-Step "Starting frontend"
$frontend = Start-LoggedProcess `
  -Name "frontend" `
  -FilePath $CondaCommand `
  -Arguments $frontendArgs `
  -WorkingDirectory $WebRoot `
  -Stdout $frontendOut `
  -Stderr $frontendErr

Wait-Http -Name "Backend" -Url "http://${BackendHost}:${BackendPort}/api/health" -ErrorLog $backendErr
Wait-Http -Name "Frontend" -Url "http://127.0.0.1:${FrontendPort}/" -ErrorLog $frontendErr

$listeners = Get-NetTCPConnection -LocalPort @($BackendPort, $FrontendPort) -State Listen -ErrorAction SilentlyContinue |
  Select-Object LocalAddress, LocalPort, State, OwningProcess,
    @{Name = "ProcessName"; Expression = { (Get-Process -Id $_.OwningProcess -ErrorAction SilentlyContinue).ProcessName } }

Write-Host ""
Write-Step "Started"
Write-Host "  Backend:  http://${BackendHost}:${BackendPort}"
Write-Host "  Frontend: http://127.0.0.1:${FrontendPort}"
Write-Host "  Backend launcher PID:  $($backend.Id)"
Write-Host "  Frontend launcher PID: $($frontend.Id)"
Write-Host "  Logs:"
Write-Host "    $backendOut"
Write-Host "    $backendErr"
Write-Host "    $frontendOut"
Write-Host "    $frontendErr"
Write-Host ""
$listeners | Format-Table -AutoSize
