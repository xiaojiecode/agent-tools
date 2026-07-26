#requires -Version 7.0

[CmdletBinding()]
param(
    [string]$CodexHome,
    [string]$GlobalAgentsPath,
    [switch]$SkipGlobalAgents,
    [switch]$SkipUserEnvironment
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if ($PSVersionTable.PSEdition -ne "Core" -or $PSVersionTable.PSVersion.Major -lt 7) {
    throw "windows-safe-tools requires PowerShell 7 or newer (pwsh.exe)."
}

if ([string]::IsNullOrWhiteSpace($CodexHome)) {
    $CodexHome = if ($env:CODEX_HOME) {
        $env:CODEX_HOME
    } else {
        Join-Path ([Environment]::GetFolderPath("UserProfile")) ".codex"
    }
}

$CodexHome = [IO.Path]::GetFullPath($CodexHome)
if ([string]::IsNullOrWhiteSpace($GlobalAgentsPath)) {
    $GlobalAgentsPath = Join-Path $CodexHome "AGENTS.md"
}
$GlobalAgentsPath = [IO.Path]::GetFullPath($GlobalAgentsPath)

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$goSource = Join-Path $PSScriptRoot "codex-tools.go"
$binDirectory = Join-Path $CodexHome "bin"
$skillDirectory = Join-Path $CodexHome "skills\windows-safe-tools"
$executableNames = @(
    "codex-tools.exe",
    "codex-rg.exe",
    "codex-gc.exe",
    "codex-go-outline.exe",
    "codex-ap.exe",
    "codex-status.exe",
    "codex-diff.exe",
    "codex-ps.exe"
)

function Normalize-PathEntry {
    param([string]$Value)

    if ([string]::IsNullOrWhiteSpace($Value)) {
        return ""
    }
    return [IO.Path]::GetFullPath($Value.Trim().Trim('"')).TrimEnd('\')
}

function Add-PathEntryOnce {
    param(
        [string]$PathValue,
        [string]$Entry
    )

    $normalizedEntry = Normalize-PathEntry $Entry
    $kept = foreach ($item in @($PathValue -split ';')) {
        if ([string]::IsNullOrWhiteSpace($item)) {
            continue
        }
        if (-not [string]::Equals((Normalize-PathEntry $item), $normalizedEntry, [StringComparison]::OrdinalIgnoreCase)) {
            $item.Trim()
        }
    }
    return (@($kept) + $Entry) -join ';'
}

function Update-GlobalAgents {
    param([string]$Path)

    $beginMarker = "<!-- windows-safe-tools:start -->"
    $endMarker = "<!-- windows-safe-tools:end -->"
    $block = @'
<!-- windows-safe-tools:start -->
## Windows Safe Tools

- For routine Windows repository work, prefer the executables in `%CODEX_TOOLS_HOME%` over raw PowerShell pipelines.
- Invoke installed `codex-*` short names directly. Do not concatenate `$env:CODEX_TOOLS_HOME` with an executable name unless `Get-Command` first confirms the short name is unavailable; a computed path must be invoked with `&`.
- `codex-ps` requires PowerShell 7 (`pwsh.exe`) and runs with `-NoProfile -ExecutionPolicy Bypass`; never use Windows PowerShell 5.1 as a fallback.
- Use `codex-gc` for bounded UTF-8 reads, `codex-rg` for excluded searches, `codex-go-outline` for Go declarations, and `codex-ap` for UTF-8 patch files.
- Use `codex-status` and `codex-diff` for Git checks. For Agent-authored PowerShell, pipe a literal here-string directly to `codex-ps`; keep direct `codex-ps '<script>'` only for trivial one-argument commands.
- Keep reusable Windows/PowerShell workarounds in the global `AGENTS.md` or the `windows-safe-tools` skill instead of duplicating them in project-level instructions.
<!-- windows-safe-tools:end -->
'@

    $parent = Split-Path -Parent $Path
    New-Item -ItemType Directory -Path $parent -Force | Out-Null
    $existing = if (Test-Path -LiteralPath $Path) {
        Get-Content -LiteralPath $Path -Raw -Encoding utf8
    } else {
        ""
    }

    $pattern = "(?ms)^" + [Regex]::Escape($beginMarker) + ".*?^" + [Regex]::Escape($endMarker) + "\r?\n?"
    if ([Regex]::IsMatch($existing, $pattern)) {
        $regex = [Regex]::new($pattern)
        $updated = $regex.Replace($existing, $block + [Environment]::NewLine, 1)
    } elseif ([string]::IsNullOrWhiteSpace($existing)) {
        $updated = $block + [Environment]::NewLine
    } else {
        $updated = $existing.TrimEnd() + [Environment]::NewLine + [Environment]::NewLine + $block + [Environment]::NewLine
    }
    Set-Content -LiteralPath $Path -Value $updated -Encoding utf8 -NoNewline
}

$goCommand = Get-Command go -CommandType Application -ErrorAction Stop | Select-Object -First 1
$temporaryDirectory = Join-Path ([IO.Path]::GetTempPath()) ("windows-safe-tools-" + [Guid]::NewGuid().ToString("N"))
$temporaryBinary = Join-Path $temporaryDirectory "codex-tools.exe"

try {
    New-Item -ItemType Directory -Path $temporaryDirectory -Force | Out-Null
    & $goCommand.Source build -trimpath '-ldflags=-s -w' -o $temporaryBinary $goSource
    if ($LASTEXITCODE -ne 0) {
        throw "go build failed with exit code $LASTEXITCODE"
    }

    New-Item -ItemType Directory -Path $binDirectory -Force | Out-Null
    foreach ($name in $executableNames) {
        Copy-Item -LiteralPath $temporaryBinary -Destination (Join-Path $binDirectory $name) -Force
    }

    if (-not [string]::Equals([IO.Path]::GetFullPath($repositoryRoot), [IO.Path]::GetFullPath($skillDirectory), [StringComparison]::OrdinalIgnoreCase)) {
        New-Item -ItemType Directory -Path $skillDirectory -Force | Out-Null
        foreach ($item in @("SKILL.md", "agents", "scripts")) {
            Copy-Item -LiteralPath (Join-Path $repositoryRoot $item) -Destination $skillDirectory -Recurse -Force
        }
    }

    if (-not $SkipUserEnvironment) {
        [Environment]::SetEnvironmentVariable("CODEX_TOOLS_HOME", $binDirectory, "User")
        $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
        [Environment]::SetEnvironmentVariable("Path", (Add-PathEntryOnce $userPath $binDirectory), "User")
        $env:CODEX_TOOLS_HOME = $binDirectory
        $env:PATH = Add-PathEntryOnce $env:PATH $binDirectory
    }

    if (-not $SkipGlobalAgents) {
        Update-GlobalAgents $GlobalAgentsPath
    }
} finally {
    if (Test-Path -LiteralPath $temporaryDirectory) {
        Remove-Item -LiteralPath $temporaryDirectory -Recurse -Force
    }
}

[pscustomobject]@{
    PowerShell = $PSVersionTable.PSVersion.ToString()
    CodexHome = $CodexHome
    Tools = $binDirectory
    Skill = $skillDirectory
    GlobalAgents = if ($SkipGlobalAgents) { "skipped" } else { $GlobalAgentsPath }
}
