---
name: agent-tools
description: Use Windows-native agent helper commands for bounded UTF-8 file reads with glob expansion and batch boundaries, noise-excluded ripgrep searches, direct apply_patch delivery, Git checks, kill-on-close child processes, and profile-free PowerShell 7 execution. Use for routine repository work on Windows when quoting, encoding, oversized files or lines, profile noise, shell metacharacters, or orphaned child processes make raw PowerShell commands fragile.
---

# Agent Tools

Invoke the installed `agent-*` executable names directly. The installer places `%AGENT_TOOLS_HOME%` on `PATH`; use a computed path only with PowerShell's `&` call operator.

## Read Files

Use `agent-read` for bounded UTF-8 reads:

```powershell
agent-read <path-or-pattern>...
agent-read <paths...> --lines START:END
agent-read <paths...> --from N --count N
agent-read <paths...> --head N
agent-read <paths...> --tail N
agent-read <paths...> --number
agent-read <paths...> --all
agent-read <paths...> --max-lines N
```

Pass multiple concrete paths or patterns using `*`, `?`, `[]`, or recursive `**`. Results are sorted and deduplicated. Prefix a path beginning with `-` by `--`.

The default output is the first 2,000 lines. Follow the stderr continuation hint when more content exists. Explicit ranges and `--tail` are complete unless `--max-lines` is supplied.

For multiple files, recognize these boundaries and use the JSON metadata to associate content with its source:

```text
<<<AGENT_READ_FILE_START {"index":1,"total":2,"path":"C:\\code\\a.txt"}>>>
...
<<<AGENT_READ_FILE_END {"index":1,"total":2,"path":"C:\\code\\a.txt","status":"ok"}>>>
```

Treat invalid UTF-8 and safety-limit errors as failed reads. Limits are 1,000 matched files, 512 MiB per file, 8 MiB per line, 128 MiB retained tail content, and 1,000,000 tail lines. `~`, `~/...`, and `~\...` expand to the current user profile.

## Search Repositories

Use `agent-rg <pattern> [roots...]`. It invokes Windows `rg.exe` with line numbers, smart case, safe pattern separation, and exclusions for `node_modules`, `dist`, `logs`, `.git`, `.idea`, `tmp`, `.cache`, and `coverage`.

## Apply Patches

Use `agent-ap <patch-file>`. Supply a valid UTF-8 patch beginning with `*** Begin Patch` at byte zero. The helper passes it directly to `codex.exe --codex-run-as-apply-patch` without an intermediate shell.

Codex CLI discovery order is `CODEX_EXE`, `%LOCALAPPDATA%\Programs\OpenAI\Codex\bin\codex.exe`, then `codex.exe` in `PATH`.

## Check Git State

Use `agent-status [repo]` for `git -C <repo> status --short`.

Use `agent-diff [repo] [paths...]`. With no paths it prints `git diff --stat`; with paths it adds an argument-array `--` separator.

## Run PowerShell

Pipe Agent-authored scripts as literal here-strings to keep source out of command-line parsing:

```powershell
@'
param([string]$Text)
$payload = '{"enabled":true}'
Write-Output "$Text $payload"
'@ | agent-ps -- -Text 'a|b & 中文 "quote"'
```

Use `agent-ps --file <script.ps1> [-- args...]` for existing files. Use direct `agent-ps '<script>'` only for trivial one-line scripts already represented by one caller argument.

`agent-ps` requires `pwsh.exe` and always supplies `-NoProfile -ExecutionPolicy Bypass`. It validates piped UTF-8, uses a temporary `.ps1`, and removes matching temporary files older than one hour. It does not fall back to Windows PowerShell 5.1.

## Safety And Dependencies

- External processes receive argument arrays rather than `.cmd` forwarding.
- External processes join a `KILL_ON_JOB_CLOSE` Windows Job Object when available.
- Missing dependencies return 127; usage errors return 2; file and parse failures normally return 1.
- `agent-read` is self-contained. `agent-rg` requires `rg.exe`; Git commands require Git; `agent-ps` requires PowerShell 7; `agent-ap` requires Codex CLI.

## Install

Run `pwsh -NoProfile -File scripts/install.ps1` from the repository root. The installer builds seven executable names, sets `AGENT_TOOLS_HOME`, installs the Skill under `%CODEX_HOME%\skills\agent-tools`, and updates the global `AGENTS.md` marker block.

Standard layout:

```text
%USERPROFILE%\.codex\bin\
  agent-tools.exe
  agent-read.exe
  agent-rg.exe
  agent-ap.exe
  agent-status.exe
  agent-diff.exe
  agent-ps.exe

%USERPROFILE%\.codex\skills\agent-tools\
  SKILL.md
  agents\openai.yaml
  scripts\agent-tools.go
  scripts\agent-tools_test.go
```
