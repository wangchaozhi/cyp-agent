# regression.ps1 — OKX Demo 全链路回归（实盘发布前强制执行）
#
# 覆盖链路：启动 → health/ready → run 分析 → 人工审批 → 持仓核验 → 平仓 →
# 对账 → 审计导出。任何一步失败即退出非零，禁止带病上实盘。
#
# 前置条件（Demo 环境）：
#   CYP_MODE=paper 或 live 前的演练配置，CYP_EXECUTION_VENUE=okx，CYP_OKX_DEMO=true，
#   OKX Demo 凭据（OKX_API_KEY/OKX_API_SECRET/OKX_PASSWORD）已配置。
#
# 用法：
#   pwsh scripts/regression.ps1                       # 构建并启动临时后端，跑完自动关停
#   pwsh scripts/regression.ps1 -UseRunning -Port 8000  # 复用已运行后端
#   pwsh scripts/regression.ps1 -Symbol "ETH/USDT:USDT"

[CmdletBinding()]
param(
  [string]$BackendHost = "127.0.0.1",
  [int]$Port = 8320,
  [string]$Symbol = "",
  [switch]$UseRunning,
  [int]$RunTimeoutSec = 300,
  [string]$ApiToken = $env:CYP_API_TOKEN
)

$ErrorActionPreference = "Stop"
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$OutputEncoding = [System.Text.UTF8Encoding]::new($false)

$ProjectRoot = Split-Path -Parent $PSScriptRoot
$LogDir = Join-Path $ProjectRoot ".logs"
New-Item -ItemType Directory -Force -Path $LogDir | Out-Null
$Base = "http://${BackendHost}:${Port}"
$script:Failures = @()
$script:Backend = $null

function Write-Step { param([string]$Message) Write-Host "[regression] $Message" }
function Add-Failure {
  param([string]$Message)
  $script:Failures += $Message
  Write-Host "[regression] FAIL: $Message" -ForegroundColor Red
}

function Invoke-Api {
  param(
    [string]$Method = "GET",
    [string]$Path,
    $Body = $null,
    [int]$TimeoutSec = 30
  )
  $headers = @{}
  if ($ApiToken) { $headers["X-CYP-API-Token"] = $ApiToken }
  $arguments = @{
    Method = $Method; Uri = "$Base$Path"; Headers = $headers
    TimeoutSec = $TimeoutSec; UseBasicParsing = $true
  }
  if ($null -ne $Body) {
    $arguments["ContentType"] = "application/json; charset=utf-8"
    $arguments["Body"] = [System.Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Depth 8))
  }
  return Invoke-RestMethod @arguments
}

function Wait-Terminal-Run {
  param([string]$RunId)
  $deadline = (Get-Date).AddSeconds($RunTimeoutSec)
  $approved = $false
  while ((Get-Date) -lt $deadline) {
    $result = Invoke-Api -Path "/api/runs/$RunId"
    if ($result.status -notin @("queued", "running")) { return $result }
    if (-not $approved) {
      # 人工审批链路：出现在待审批队列时批准，覆盖 approvals API。
      $pendingList = @(Invoke-Api -Path "/api/pending")
      $mine = $pendingList | Where-Object { $_.run_id -eq $RunId }
      if ($mine) {
        Write-Step "审批 run $RunId (approve)"
        Invoke-Api -Method POST -Path "/api/approvals/$RunId" -Body @{
          decision = "approve"; operator = "regression-script"; note = "demo regression"
        } | Out-Null
        $approved = $true
      }
    }
    Start-Sleep -Milliseconds 800
  }
  throw "run $RunId 在 ${RunTimeoutSec}s 内未结束"
}

try {
  # ---- 0. 启动后端（或复用） -------------------------------------------------
  if (-not $UseRunning) {
    $binary = Join-Path $LogDir "cyp-server-regression.exe"
	$stateFile = Join-Path $LogDir "regression-state.json"
	Remove-Item -LiteralPath $stateFile -Force -ErrorAction SilentlyContinue
    Write-Step "构建 cyp-server → $binary"
    Push-Location $ProjectRoot
    try {
      go build -o $binary ./cmd/cyp-server
      if ($LASTEXITCODE -ne 0) { throw "go build ./cmd/cyp-server 失败" }
    } finally { Pop-Location }
    $stdout = Join-Path $LogDir "regression.out.log"
    $stderr = Join-Path $LogDir "regression.err.log"
    "" | Set-Content -LiteralPath $stdout -Encoding utf8
    "" | Set-Content -LiteralPath $stderr -Encoding utf8
    Write-Step "启动后端 $Base"
    $script:Backend = Start-Process -FilePath $binary `
      -ArgumentList @("-host", $BackendHost, "-port", "$Port") `
      -WorkingDirectory $ProjectRoot -WindowStyle Hidden `
      -Environment @{
        CYP_MODE = "paper"
        CYP_EXECUTION_VENUE = "okx"
        CYP_OKX_DEMO = "true"
        CYP_RUNTIME_AUTOSTART = "false"
        CYP_AUTOMATION_ENABLED = "false"
        CYP_PERSISTENCE = "file"
        CYP_STATE_FILE = $stateFile
      } `
      -RedirectStandardOutput $stdout -RedirectStandardError $stderr -PassThru
  }

  # ---- 1. health / ready -----------------------------------------------------
  $health = $null
  for ($attempt = 0; $attempt -lt 60 -and -not $health; $attempt++) {
    try { $health = Invoke-Api -Path "/api/health" -TimeoutSec 2 } catch { Start-Sleep -Milliseconds 500 }
  }
  if (-not $health) { throw "后端未就绪：$Base/api/health 无响应" }
  Write-Step "health: mode=$($health.mode) display=$($health.display_mode) venue=$($health.execution_venue) kill=$($health.kill)"
  if ($health.kill) { Add-Failure "Kill Switch 处于开启状态，回归要求干净环境" }

  # 这个脚本会真实调用执行接口，只允许 OKX Demo。即使操作员的 .env
  # 已切到 live，也必须在任何 run 之前 fail-closed，绝不把回归变成实盘下单。
  $settings = Invoke-Api -Path "/api/settings"
  if ($health.mode -ne "paper" -or $health.execution_venue -ne "okx" -or -not $settings.okx.demo) {
    throw "安全拒绝：regression.ps1 仅允许 mode=paper + execution_venue=okx + okx.demo=true"
  }

  $ready = $null
  $readyDeadline = (Get-Date).AddSeconds(120)
  while ((Get-Date) -lt $readyDeadline) {
    $ready = Invoke-Api -Path "/api/ready"
    if ($ready.ready -and -not $ready.reconciling) { break }
    Start-Sleep -Seconds 2
  }
  if (-not $ready.ready) {
    Add-Failure "ready=false（safety=$($ready.safety.reason)；reasons=$($ready.reasons -join '; ')）"
    throw "启动对账未通过，终止回归"
  }
  Write-Step "ready: execution_ready=$($ready.execution_ready)"

	$initialPositions = @(
	  (Invoke-Api -Path "/api/positions") |
	    Where-Object { $null -ne $_ -and -not [string]::IsNullOrWhiteSpace($_.symbol) }
	)
  if ($initialPositions.Count -gt 0) {
    throw "安全拒绝：Demo 回归要求初始空仓，当前检测到 $($initialPositions.Count) 个持仓"
  }

  # ---- 2. run 分析 + 审批 → 开仓 ---------------------------------------------
  $watchlist = @()
  if ($Symbol) { $watchlist = @($Symbol) }
  elseif ($settings.watchlist) { $watchlist = @($settings.watchlist) }
  if (-not $watchlist) { throw "无可用交易对：请通过 -Symbol 指定或配置 watchlist" }

  $executed = $null
  foreach ($candidate in $watchlist) {
    Write-Step "run 分析 $candidate"
    $accepted = Invoke-Api -Method POST -Path "/api/run" -Body @{ symbol = $candidate }
    $result = Wait-Terminal-Run -RunId $accepted.run_id
    Write-Step "run $($accepted.run_id) → $($result.status)"
    if ($result.status -eq "executed") { $executed = $result; break }
    if ($result.status -in @("error", "execution_failed")) {
      Add-Failure "run $($accepted.run_id) 状态 $($result.status)：$($result.error)"
    }
  }
  if (-not $executed) {
    throw "watchlist 内没有任何 run 走到 executed（多为信号原因），换个行情窗口或 -Symbol 后重跑"
  }
  $tradedSymbol = $executed.proposal.symbol

  # ---- 3. 持仓核验 -----------------------------------------------------------
  $positions = @(Invoke-Api -Path "/api/positions")
  $opened = $positions | Where-Object { $_.symbol -eq $tradedSymbol }
  if (-not $opened) {
    Add-Failure "executed 后交易所无 $tradedSymbol 持仓"
    throw "持仓核验失败"
  }
  $opened = @($opened)[0]
  Write-Step "持仓核验通过：$($opened.symbol) $($opened.side) size=$($opened.size_base)"

  # 保护单/订单生命周期核验：executed 订单必须处于 protective_placed。
  $orders = @(Invoke-Api -Path "/api/orders?limit=20")
  $entry = $orders | Where-Object { $_.intent.symbol -eq $tradedSymbol } | Select-Object -First 1
  if ($entry -and $entry.status -notin @("protective_placed")) {
    Add-Failure "入场订单状态为 $($entry.status)，预期 protective_placed（保护单核验未通过）"
  }

  # ---- 4. 平仓 ---------------------------------------------------------------
  Write-Step "平仓 $tradedSymbol"
  Invoke-Api -Method POST -Path "/api/positions/close" -Body @{
    symbol = $tradedSymbol; instrument = $opened.instrument
  } -TimeoutSec 120 | Out-Null
  $after = @(Invoke-Api -Path "/api/positions") | Where-Object { $_.symbol -eq $tradedSymbol }
  if ($after) { Add-Failure "平仓后仍有 $tradedSymbol 持仓" }
  else { Write-Step "平仓完成，仓位已清" }

  # ---- 5. 对账 ---------------------------------------------------------------
  Write-Step "触发运行时对账"
  $report = Invoke-Api -Method POST -Path "/api/reconcile" -Body @{} -TimeoutSec 60
  if (-not $report.ok) { Add-Failure "对账未通过：$($report | ConvertTo-Json -Depth 5 -Compress)" }
  else { Write-Step "对账通过" }

  # ---- 6. 审计导出 -----------------------------------------------------------
  $auditPath = Join-Path $LogDir "regression-audit.json"
  $audit = Invoke-Api -Path "/api/audit/export" -TimeoutSec 60
  $audit | ConvertTo-Json -Depth 16 | Set-Content -LiteralPath $auditPath -Encoding utf8
  if (-not $audit.orders) { Add-Failure "审计导出不含订单流水" }
  else { Write-Step "审计导出完成：$auditPath（orders=$(@($audit.orders).Count) trades=$(@($audit.trades).Count)）" }
}
finally {
  if ($script:Backend -and -not $script:Backend.HasExited) {
    Write-Step "关停临时后端 PID $($script:Backend.Id)"
    taskkill /PID $script:Backend.Id /T /F | Out-Null
  }
}

Write-Host ""
if ($script:Failures.Count -gt 0) {
  Write-Host "[regression] 回归失败（$($script:Failures.Count) 项）：" -ForegroundColor Red
  $script:Failures | ForEach-Object { Write-Host "  - $_" -ForegroundColor Red }
  exit 1
}
Write-Host "[regression] 全链路回归通过：health/ready → run → 审批 → 持仓 → 平仓 → 对账 → 审计导出" -ForegroundColor Green
exit 0
