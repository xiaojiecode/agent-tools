#requires -Version 7.0

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Assert-True {
    param(
        [bool]$Condition,
        [string]$Message
    )

    if (-not $Condition) {
        throw $Message
    }
}

$installer = Join-Path $PSScriptRoot "install.ps1"
$temporaryRoot = Join-Path ([IO.Path]::GetTempPath()) ("agent-tools-install-test-" + [Guid]::NewGuid().ToString("N"))
$codexHome = Join-Path $temporaryRoot ".codex"
$globalAgents = Join-Path $temporaryRoot "AGENTS.md"

try {
    New-Item -ItemType Directory -Path $temporaryRoot -Force | Out-Null
    @'
# Existing Rules

Keep this content.

<!-- agent-tools:start -->
legacy block
<!-- agent-tools:end -->
'@ | Set-Content -LiteralPath $globalAgents -Encoding utf8 -NoNewline

    & pwsh.exe -NoProfile -File $installer -CodexHome $codexHome -GlobalAgentsPath $globalAgents -SkipUserEnvironment | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "First installer run failed with exit code $LASTEXITCODE"
    }

    $firstInstall = Get-Content -LiteralPath $globalAgents -Raw -Encoding utf8
    Assert-True ($firstInstall.Contains("Keep this content.")) "Installer removed unrelated global AGENTS content."
    Assert-True (-not $firstInstall.Contains("legacy block")) "Installer did not replace the previous marker block."
    Assert-True ($firstInstall.Contains("This section is the self-contained operating reference")) "Installed block is missing its self-contained usage rule."
    Assert-True ($firstInstall.Contains("### Command Map")) "Installed block is missing the command map."
    Assert-True ($firstInstall.Contains("### Safety And Dependencies")) "Installed block is missing safety and dependency guidance."
    Assert-True (([Regex]::Matches($firstInstall, [Regex]::Escape("<!-- agent-tools:start -->"))).Count -eq 1) "Installed file contains multiple start markers."
    Assert-True (([Regex]::Matches($firstInstall, [Regex]::Escape("<!-- agent-tools:end -->"))).Count -eq 1) "Installed file contains multiple end markers."

    $installedTemplate = Join-Path $codexHome "skills\agent-tools\assets\global-agents.md"
    Assert-True (Test-Path -LiteralPath $installedTemplate -PathType Leaf) "Installer did not copy the global AGENTS template into the installed skill."

    & pwsh.exe -NoProfile -File $installer -CodexHome $codexHome -GlobalAgentsPath $globalAgents -SkipUserEnvironment | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Second installer run failed with exit code $LASTEXITCODE"
    }

    $secondInstall = Get-Content -LiteralPath $globalAgents -Raw -Encoding utf8
    Assert-True ($secondInstall -eq $firstInstall) "Installer is not idempotent."

    Write-Output "install integration test passed"
} finally {
    if (Test-Path -LiteralPath $temporaryRoot) {
        Remove-Item -LiteralPath $temporaryRoot -Recurse -Force
    }
}
