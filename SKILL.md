---
name: windows-safe-tools
description: Use Windows-native helper commands for bounded UTF-8 reads, noise-excluded ripgrep searches, Go outlines, apply_patch, Git checks, kill-on-close child processes, and profile-free PowerShell 7 execution. Use for routine Codex repository work on Windows where quoting, encoding, profile noise, shell metacharacters, or orphaned child processes make raw PowerShell commands fragile.
---

# Windows Safe Tools

Use the executables in `%CODEX_TOOLS_HOME%` for routine Windows repository work. The eight executable names are ordinary copies of the same binary; dispatch depends on `argv[0]`, so do not replace them with `.cmd` forwarding wrappers.

## Commands

### Bounded UTF-8 reads

Use `codex-gc` instead of unbounded reads for large text files:

```powershell
codex-gc <path>
codex-gc <path> --lines START:END
codex-gc <path> --from N --count N
codex-gc <path> --head N
codex-gc <path> --tail N
codex-gc <path> --number
codex-gc <path> --all
codex-gc <path> --max-lines N
```

The default output is the first 2,000 lines. Explicit ranges and `--tail` are complete unless `--max-lines` is also supplied. Invalid UTF-8 is an error. `~`, `~/...`, and `~\...` expand to the current user profile.

### Repository search

Use `codex-rg <pattern> [roots...]`. It invokes Windows `rg.exe` with line numbers, smart case, safe `--` pattern separation, and exclusions for `node_modules`, `dist`, `logs`, `.git`, `.idea`, `tmp`, `.cache`, and `coverage`. An omitted root means the current directory.

### Go declarations

Use `codex-go-outline <file.go> [--exported] [--json]` to inspect imports, types, structs, interfaces, constants, variables, functions, methods, member signatures, documentation summaries, and source lines without printing function bodies.

### Apply a patch

Use `codex-ap <patch-file>`. The patch must be valid UTF-8 and begin with `*** Begin Patch` at byte zero. The helper passes the text directly to `codex.exe --codex-run-as-apply-patch`; it does not parse it or invoke an intermediate shell. Never put credentials or unrelated private data in patch files.

Codex CLI discovery order is `CODEX_EXE`, `%LOCALAPPDATA%\Programs\OpenAI\Codex\bin\codex.exe`, then `codex.exe` in `PATH`.

### Git checks

Use `codex-status [repo]` for `git -C <repo> status --short`.

Use `codex-diff [repo] [paths...]`. With no paths it prints `git diff --stat`; with paths it uses an argument-array `--` separator. The first argument is a repository only when it exists and is a directory.

### Profile-free PowerShell

Use these forms:

```powershell
codex-ps <script>
codex-ps --stdin [-- args...]
codex-ps --file <script.ps1> [-- args...]
```

The helper requires PowerShell 7 through `pwsh.exe`; it never falls back to Windows PowerShell 5.1. It always supplies `-NoProfile -ExecutionPolicy Bypass`. `--stdin` validates UTF-8, creates a temporary `.ps1`, removes it afterward, and cleans matching temporary files older than one hour. A standalone `--` separates helper arguments from script arguments and is not passed through.

## Safety properties

- External processes receive argument arrays; shell metacharacters are not re-parsed by `.cmd` wrappers.
- External processes are assigned to a Windows Job Object with `KILL_ON_JOB_CLOSE` when Windows permits it.
- Missing external dependencies return exit code 127.
- Usage and unknown-option errors return exit code 2.
- File and parse failures normally return exit code 1.
- The helper set does not include Codex authentication, state, logs, memories, plugin caches, project secrets, or the Codex CLI binary.

## Dependencies

`codex-gc` and `codex-go-outline` are self-contained. `codex-rg` requires Windows `rg.exe`; `codex-status` and `codex-diff` require Git; `codex-ps` requires PowerShell 7 (`pwsh.exe`); `codex-ap` requires a native `codex.exe` discoverable by its documented lookup order.

## Installation

Run `pwsh -NoProfile -File scripts/install.ps1` from the repository root. The installer builds the Go binary, creates all eight executable names, sets the current user's `CODEX_TOOLS_HOME` and `PATH`, installs the skill, and inserts an idempotent marked description block into the user's global `%CODEX_HOME%\AGENTS.md` (or `%USERPROFILE%\.codex\AGENTS.md`).

## Installation layout

The standard layout is:

```text
%USERPROFILE%\.codex\bin\
  codex-tools.exe
  codex-rg.exe
  codex-gc.exe
  codex-go-outline.exe
  codex-ap.exe
  codex-status.exe
  codex-diff.exe
  codex-ps.exe

%USERPROFILE%\.codex\skills\windows-safe-tools\
  SKILL.md
  agents\openai.yaml
  scripts\codex-tools.go
  scripts\codex-tools_test.go
```

`CODEX_TOOLS_HOME` points to the bin directory, which must also occur exactly once in the current user's `PATH`. New environment-variable values are visible to newly opened processes; already-running terminals may require a manual process-level refresh.
