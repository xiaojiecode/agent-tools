<!-- agent-tools:start -->
## Agent Tools

This section is the self-contained operating reference for routine Windows repository work. Use it directly; do not open the `agent-tools` `SKILL.md` merely to learn normal command usage. Read that skill only when maintaining or reinstalling the tools, diagnosing behavior not covered here, or when a higher-priority instruction explicitly requires it.

- Prefer the installed `%AGENT_TOOLS_HOME%` executables over fragile raw PowerShell pipelines.
- Invoke installed `agent-*` short names directly. Invoke a computed executable path with PowerShell's `&` call operator.
- Keep reusable Windows and PowerShell workarounds in the global `AGENTS.md` or the tool implementation instead of duplicating them in project-level instructions.

### Command Map

| Command | Purpose |
| --- | --- |
| `agent-read` | Bounded UTF-8 reads with ranges, globs, batch boundaries, tails, and line numbers |
| `agent-rg` | Ripgrep searches with line numbers, smart case, safe argument passing, and noise exclusions |
| `agent-ap` | Direct delivery of an existing UTF-8 patch file to the Codex apply-patch implementation |
| `agent-status` | Concise Git worktree status |
| `agent-diff` | Repository or path-scoped Git diffs |
| `agent-ps` | Profile-free PowerShell 7 execution with safe script and argument transport |

### Read Files

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

- Pass multiple concrete paths or patterns using `*`, `?`, `[]`, or recursive `**`. Results are sorted and deduplicated. Prefix a path beginning with `-` by `--`.
- The default output is the first 2,000 lines. Follow the stderr continuation hint when more content exists. Explicit ranges and `--tail` are complete unless `--max-lines` is supplied.
- Multiple-file output uses `<<<AGENT_READ_FILE_START {...}>>>` and `<<<AGENT_READ_FILE_END {...}>>>` boundaries. Associate content with its source through the JSON `path` field.
- Treat invalid UTF-8 and safety-limit errors as failed reads. Limits are 1,000 matched files, 512 MiB per file, 8 MiB per line, 128 MiB retained tail content, and 1,000,000 tail lines.
- `~`, `~/...`, and `~\...` expand to the current user profile.

### Search Repositories

- Use `agent-rg <pattern> [roots...]`. It invokes `rg.exe` with line numbers, smart case, safe pattern separation, and exclusions for `node_modules`, `dist`, `logs`, `.git`, `.idea`, `tmp`, `.cache`, and `coverage`.

### Apply Patches

- Use `agent-ap <patch-file>` for an existing UTF-8 patch file. The patch must begin with `*** Begin Patch` at byte zero and is delivered directly to Codex without an intermediate shell.
- Codex CLI discovery order is `CODEX_EXE`, `%LOCALAPPDATA%\Programs\OpenAI\Codex\bin\codex.exe`, then `codex.exe` on `PATH`.
- For ordinary manual edits made through the current tool API, continue to use the available `apply_patch` tool directly; do not create a temporary patch file unless needed.

### Check Git State

- Use `agent-status [repo]` for `git -C <repo> status --short`.
- Use `agent-diff [repo] [paths...]`. With no paths it prints a diff summary; supplied paths are passed after Git's `--` separator.

### Run PowerShell

- Use `agent-ps` for Agent-authored PowerShell. It requires `pwsh.exe` and runs with `-NoProfile -ExecutionPolicy Bypass`; never fall back to Windows PowerShell 5.1.
- Pipe non-trivial scripts as a literal here-string directly to `agent-ps`, then pass script arguments after `--`:

```powershell
@'
param([string]$Text)
$payload = '{"enabled":true}'
Write-Output "$Text $payload"
'@ | agent-ps -- -Text 'a|b & 中文 "quote"'
```

- Use `agent-ps --file <script.ps1> [-- args...]` for an existing script. Reserve direct `agent-ps '<script>'` for trivial one-line commands represented by one caller argument.
- Piped input must be valid UTF-8. Temporary scripts are removed after execution, along with matching stale temporary files older than one hour.

### Safety And Dependencies

- External processes receive argument arrays rather than `.cmd` forwarding and join a `KILL_ON_JOB_CLOSE` Windows Job Object when available.
- Missing dependencies return exit code 127; usage errors return 2; file and parse failures normally return 1.
- `agent-read` is self-contained. `agent-rg` requires `rg.exe`, Git commands require Git, `agent-ps` requires PowerShell 7, and `agent-ap` requires the Codex CLI.
<!-- agent-tools:end -->
