# Agent Tools

一组供 Agent 在 Windows 上稳定执行仓库操作的原生辅助命令。七个命令名由同一个 Go 二进制文件按 `argv[0]` 分发，避免 `.cmd` 转发造成的二次解析、引号破坏和 shell 元字符问题。

## 命令

| 命令 | 用途 |
| --- | --- |
| `agent-read` | 有界 UTF-8 文件读取，支持范围、通配符、批量分隔、尾部和行号 |
| `agent-rg` | 带常用噪声目录排除的 `rg -n -S` |
| `agent-ap` | 将 UTF-8 补丁直接传给 Codex apply-patch |
| `agent-status` | 干净的 `git status --short` |
| `agent-diff` | 仓库或指定路径的 Git diff |
| `agent-ps` | 使用 PowerShell 7 进行 profile-free 脚本执行 |
| `agent-tools` | 通过第一个参数调用上述子命令 |

外部进程会在 Windows 允许时加入 `KILL_ON_JOB_CLOSE` Job Object。

## Agent Read

`agent-read` 默认输出前 2,000 行，并在存在更多内容时给出下一段参数。显式范围按需单次扫描，`--tail` 使用环形缓冲。

```powershell
agent-read .\README.md
agent-read .\large.log --from 2001 --count 2000 --number
agent-read '.\src\*.go' --head 120
agent-read '.\src\**\*.go' '.\docs\*.md' --lines 20:80
agent-read .\large.log --tail 200
```

支持多个具体路径以及 `*`、`?`、`[]`、递归 `**`。匹配结果稳定排序并去重。读取多个文件时，输出使用带 JSON 元数据的边界：

```text
<<<AGENT_READ_FILE_START {"index":1,"total":2,"path":"C:\\code\\a.txt"}>>>
file content
<<<AGENT_READ_FILE_END {"index":1,"total":2,"path":"C:\\code\\a.txt","status":"ok"}>>>
```

安全上限：一次最多 1,000 个文件、单文件 512 MiB、单行 8 MiB、tail 保留内容 128 MiB、tail 最多 100 万行。无效 UTF-8、超限文件和超长行都会返回错误。

## 要求

- Windows 10/11。
- PowerShell 7，命令名必须为 `pwsh.exe`。不回退到 Windows PowerShell 5.1。
- Go，用于从源码安装。
- 可选依赖：Ripgrep、Git、Codex CLI，分别由对应命令使用。

## 安装

```powershell
git clone https://github.com/xiaojiecode/agent-tools.git
Set-Location .\agent-tools
pwsh -NoProfile -File .\scripts\install.ps1
```

安装脚本会构建七个可执行文件，设置当前用户的 `AGENT_TOOLS_HOME` 和 `PATH`，将 Skill 安装到 `%CODEX_HOME%\skills\agent-tools`，并在全局 `AGENTS.md` 中写入 `agent-tools` 标记块。

测试安装而不修改用户环境变量或真实全局 Agent 文件：

```powershell
$testHome = Join-Path $env:TEMP "agent-tools-test"
pwsh -NoProfile -File .\scripts\install.ps1 `
  -CodexHome $testHome `
  -SkipUserEnvironment
```

## PowerShell 脚本

Agent 编写的复杂脚本使用字面 here-string 通过标准输入传递：

```powershell
@'
param([string]$Text)
Write-Output "Text=$Text"
'@ | agent-ps -- -Text 'a|b & 中文 "quote"'
```

安装后直接调用短命令。必须使用计算路径时，用 PowerShell 调用运算符：

```powershell
& (Join-Path $env:AGENT_TOOLS_HOME 'agent-ps.exe') 'Get-Date'
```

## 开发验证

```powershell
go test .\scripts
go build -o .\dist\agent-tools.exe .\scripts\agent-tools.go
pwsh -NoProfile -File .\scripts\install.ps1 -CodexHome (Join-Path $env:TEMP "agent-tools-test") -SkipUserEnvironment
```

## License

MIT
