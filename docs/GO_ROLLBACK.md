# 历史归档使用与回退

主分支没有旧后端切换开关，也不包含旧运行时依赖。需要审计或应急验证旧实现时，只能从历史归档分支 `archive/python-backend-20260710` 创建**独立 Git worktree**。不要切换当前 `main`，不要把归档文件复制回主工作树。

归档分支的冻结提交为：

```text
36b55ddc76a49d2923c11f93e103c60669ca2969
```

## 原则

- `main` 始终保留当前 Go 代码和工作区状态；
- 归档 worktree 放在仓库目录之外；
- 使用 detached HEAD，避免误改归档分支；
- 两个工作树不得共享 `.env`、状态文件、数据库、端口或日志目录；
- 归档仅用于审计、对照或经批准的应急部署，不是主分支的运行模式；
- 不在归档分支直接提交；确需修补时从归档提交新建独立 hotfix 分支。

## 创建只读归档 worktree

在 `cyp-agent` 主仓库根目录打开 PowerShell：

```powershell
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8

$Repo = (Get-Location).Path
$ArchivePath = Join-Path (Split-Path $Repo -Parent) "cyp-agent-python-archive"
$ArchiveBranch = "archive/python-backend-20260710"
$ExpectedCommit = "36b55ddc76a49d2923c11f93e103c60669ca2969"

if ((git branch --show-current) -ne "main") {
  throw "请从 main 工作树执行；不要切换主工作树"
}
if (Test-Path -LiteralPath $ArchivePath) {
  throw "目标目录已存在：$ArchivePath"
}

git fetch origin --prune
if ($LASTEXITCODE -ne 0) { throw "git fetch 失败" }

git show-ref --verify --quiet "refs/heads/$ArchiveBranch"
if ($LASTEXITCODE -eq 0) {
  $ArchiveRef = $ArchiveBranch
} else {
  $ArchiveRef = "origin/$ArchiveBranch"
}

$ResolvedCommit = git rev-parse "$ArchiveRef`^{commit}"
if ($LASTEXITCODE -ne 0) { throw "找不到归档分支 $ArchiveBranch" }
$ResolvedCommit = "$ResolvedCommit".Trim()
if ($ResolvedCommit -ne $ExpectedCommit) {
  throw "归档引用不是冻结提交：$ResolvedCommit"
}

git worktree add --detach $ArchivePath $ArchiveRef
if ($LASTEXITCODE -ne 0) { throw "创建归档 worktree 失败" }
```

命令完成后，当前仓库仍在 `main`；归档代码位于同级目录 `cyp-agent-python-archive`。

验证两个工作树：

```powershell
git -C $Repo branch --show-current
git -C $Repo status --short
git -C $ArchivePath rev-parse HEAD
git -C $ArchivePath status --short
git worktree list
```

期望结果：主工作树分支为 `main`，归档 HEAD 等于冻结提交，归档工作树无修改。

## 使用归档

所有归档相关命令都必须显式指定归档目录，或在受控的 `Push-Location` 范围内执行：

```powershell
Push-Location $ArchivePath
try {
  git status --short
  git log -1 --oneline
  # 仅按该归档分支自身的文档进行审计、构建或应急验证。
} finally {
  Pop-Location
}
```

运行归档前应先停止当前服务或使用完全不同的端口，并为归档准备独立配置与数据。不要让归档进程连接 Go 主服务正在使用的状态文件或 PostgreSQL 库；不要复用真实凭据。任何面向外部的应急切换都必须先开启 Kill Switch、确认现有持仓/审批状态并保留审计记录。

禁止执行以下操作：

```powershell
# 禁止在主工作树切换到归档分支
git switch archive/python-backend-20260710

# 禁止把归档内容覆盖到 main
Copy-Item "$ArchivePath\*" $Repo -Recurse -Force
```

## 需要修改归档时

归档引用应保持不可变。确有修复需求时，在归档 worktree 从冻结提交创建新分支，不要移动 `archive/python-backend-20260710`：

```powershell
$Hotfix = "hotfix/archive-$(Get-Date -Format yyyyMMdd-HHmmss)"
git -C $ArchivePath switch -c $Hotfix
```

该分支必须独立评审、构建和发布，不能合并或复制进 `main`，除非另有明确的 Go 实现迁移任务。

## 清理归档 worktree

先停止从归档目录启动的所有进程，再确认没有未保存改动：

```powershell
$Changes = git -C $ArchivePath status --porcelain
if ($Changes) {
  $Changes
  throw "归档 worktree 有未保存改动，拒绝删除"
}

git worktree remove $ArchivePath
if ($LASTEXITCODE -ne 0) { throw "移除 worktree 失败" }
git worktree prune
```

不要使用 `--force` 掩盖未保存改动。清理 worktree 不会删除归档分支或其提交。

## 恢复使用 Go 主服务

主工作树从未切换分支，因此无需执行 reset、checkout 或文件恢复。停止归档进程后，回到 `$Repo`，按 [`GO_OPERATIONS.md`](GO_OPERATIONS.md) 重新构建并启动 Go 服务，验证：

```powershell
Set-Location $Repo
git branch --show-current
go test -count=1 ./...
go run ./cmd/cyp-server -host 127.0.0.1 -port 8000
```

另开终端检查 `/api/health` 与 `/api/ready`。确认启动对账成功、SafetyState 未冻结后，才继续新的 Paper 操作。
