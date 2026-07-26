# Windows Safe Tools

一组供 Codex 在 Windows 上稳定执行仓库操作的原生辅助命令。八个命令名由同一个 Go 二进制文件按 `argv[0]` 分发，避免 `.cmd` 转发造成的二次解析、引号破坏和 shell 元字符问题。

## 适用场景

- 大文件只需读取局部内容，并且必须严格验证 UTF-8。
- `rg` 搜索需要默认排除依赖、构建目录、日志和缓存噪声。
- 只想查看 Go 文件的声明、签名和行号，不希望加载全部函数体。
- 补丁包含中文、引号或 shell 元字符，需要原样交给 Codex 的 apply-patch 入口。
- Git 状态与差异检查需要参数数组和可靠的 `--` 分隔。
- PowerShell profile、提示符插件或嵌套引用会污染输出。
- 外部超时后不应遗留 `pwsh`、`git` 等子进程。

## 命令

| 命令 | 用途 |
| --- | --- |
| `codex-gc` | 有界 UTF-8 文件读取，支持范围、尾部和行号 |
| `codex-rg` | 带常用噪声目录排除的 `rg -n -S` |
| `codex-go-outline` | 提取 Go 声明、签名、注释摘要和源码行 |
| `codex-ap` | 将 UTF-8 补丁直接传给 Codex apply-patch |
| `codex-status` | 干净的 `git status --short` |
| `codex-diff` | 仓库或指定路径的 Git diff |
| `codex-ps` | 使用 PowerShell 7 进行 profile-free 脚本执行 |
| `codex-tools` | 通过第一个参数调用上述子命令 |

外部进程会在 Windows 允许时加入 `KILL_ON_JOB_CLOSE` Job Object。

## 要求

- Windows 10/11。
- PowerShell 7，命令名必须为 `pwsh.exe`。不支持回退到 Windows PowerShell 5.1。
- Go，用于从源码安装。
- 可选依赖：Ripgrep、Git、Codex CLI，分别由对应命令使用。

## 安装

```powershell
git clone https://github.com/xiaojiecode/windows-safe-tools.git
Set-Location .\windows-safe-tools
pwsh -NoProfile -File .\scripts\install.ps1
```

安装脚本会：

1. 构建 `codex-tools.exe` 并复制为八个短命令名。
2. 设置当前用户的 `CODEX_TOOLS_HOME`，并确保工具目录在用户 `PATH` 中只出现一次。
3. 安装 Skill 到 `%CODEX_HOME%\skills\windows-safe-tools`，未设置 `CODEX_HOME` 时使用 `%USERPROFILE%\.codex`。
4. 在用户全局 `AGENTS.md` 中插入或更新 `<!-- windows-safe-tools:start -->` 标记块，说明命令用途并明确 PowerShell 7 约束。

可选参数：

```powershell
pwsh -NoProfile -File .\scripts\install.ps1 `
  -CodexHome D:\portable-codex `
  -GlobalAgentsPath D:\portable-codex\AGENTS.md
```

测试安装而不修改用户环境变量或真实全局 Agent 文件：

```powershell
$testHome = Join-Path $env:TEMP "windows-safe-tools-test"
pwsh -NoProfile -File .\scripts\install.ps1 `
  -CodexHome $testHome `
  -SkipUserEnvironment
```

## 示例

```powershell
codex-gc .\large.go --lines 120:220 --number
codex-rg 'foo|bar' src
codex-go-outline .\internal\service.go --exported
codex-status C:\code\project
codex-diff C:\code\project AGENTS.md
codex-ps 'Get-Date'
```

Agent 编写的脚本默认使用字面 here-string 通过标准输入传递，参数放在独立的 `--` 之后：

```powershell
@'
param([string]$Text)
Write-Output "Text=$Text"
'@ | codex-ps -- -Text 'a|b & 中文 "quote"'
```

here-string 的起止标记必须各自独占一行，正文可以自然书写单引号、双引号、变量、JSON、换行和 shell 元字符。外层 PowerShell 单引号字符串中的 `''` 是调用方的转义语法，不是 `codex-ps` 的要求；不要用这种形式承载 Agent 编写的复杂脚本。显式 `--stdin` 仍可供非 PowerShell 调用方使用。

安装后优先直接调用 `codex-ps`。只有 `Get-Command codex-ps -CommandType Application` 确认短命令不可用时，才回退到：

```powershell
& (Join-Path $env:CODEX_TOOLS_HOME 'codex-ps.exe') 'Get-Date'
```

完整参数和安全属性见 [SKILL.md](SKILL.md)。

## 开发验证

```powershell
go test .\scripts
go build -o .\dist\codex-tools.exe .\scripts\codex-tools.go
pwsh -NoProfile -File .\scripts\install.ps1 -CodexHome (Join-Path $env:TEMP "windows-safe-tools-test") -SkipUserEnvironment
```

## License

MIT
