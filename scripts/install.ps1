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
    throw "agent-tools requires PowerShell 7 or newer (pwsh.exe)."
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
$goSource = Join-Path $PSScriptRoot "agent-tools.go"
$globalAgentsTemplate = Join-Path $repositoryRoot "assets\global-agents.md"
$binDirectory = Join-Path $CodexHome "bin"
$skillDirectory = Join-Path $CodexHome "skills\agent-tools"
$executableNames = @(
    "agent-tools.exe",
    "agent-rg.exe",
    "agent-read.exe",
    "agent-ap.exe",
    "agent-status.exe",
    "agent-diff.exe",
    "agent-ps.exe"
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
    param(
        [string]$Path,
        [string]$TemplatePath
    )

    $beginMarker = "<!-- agent-tools:start -->"
    $endMarker = "<!-- agent-tools:end -->"
    if (-not (Test-Path -LiteralPath $TemplatePath -PathType Leaf)) {
        throw "Global AGENTS template not found: $TemplatePath"
    }

    $block = (Get-Content -LiteralPath $TemplatePath -Raw -Encoding utf8).Trim()
    $beginCount = [Regex]::Matches($block, [Regex]::Escape($beginMarker)).Count
    $endCount = [Regex]::Matches($block, [Regex]::Escape($endMarker)).Count
    if ($beginCount -ne 1 -or $endCount -ne 1 -or -not $block.StartsWith($beginMarker) -or -not $block.EndsWith($endMarker)) {
        throw "Global AGENTS template must contain exactly one complete agent-tools marker block."
    }

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
$temporaryDirectory = Join-Path ([IO.Path]::GetTempPath()) ("agent-tools-" + [Guid]::NewGuid().ToString("N"))
$temporaryBinary = Join-Path $temporaryDirectory "agent-tools.exe"

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
        foreach ($item in @("SKILL.md", "agents", "assets", "scripts")) {
            Copy-Item -LiteralPath (Join-Path $repositoryRoot $item) -Destination $skillDirectory -Recurse -Force
        }
    }

    if (-not $SkipUserEnvironment) {
        [Environment]::SetEnvironmentVariable("AGENT_TOOLS_HOME", $binDirectory, "User")
        $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
        [Environment]::SetEnvironmentVariable("Path", (Add-PathEntryOnce $userPath $binDirectory), "User")
        $env:AGENT_TOOLS_HOME = $binDirectory
        $env:PATH = Add-PathEntryOnce $env:PATH $binDirectory
    }

    if (-not $SkipGlobalAgents) {
        Update-GlobalAgents $GlobalAgentsPath $globalAgentsTemplate
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
