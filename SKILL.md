---
name: agent-tools
description: Maintain, install, or troubleshoot the Windows-native agent helper commands and their global AGENTS.md integration. Use when Codex needs to change this tool package, reinstall it, diagnose behavior not covered by the installed global reference, or when the user explicitly invokes $agent-tools. Routine command usage is documented in global AGENTS.md and should not implicitly trigger this skill.
---

# Agent Tools

The installer writes the complete routine command reference from `assets/global-agents.md` into the global `AGENTS.md`. Do not load this skill merely to look up normal `agent-*` syntax; use the already-loaded global reference.

## Maintenance Workflow

1. Edit `assets/global-agents.md` when changing the Agent-facing routine usage contract.
2. Edit `scripts/agent-tools.go` and its tests when changing executable behavior.
3. Keep `scripts/install.ps1` responsible for validating and installing the template, binaries, and skill files.
4. Run the Go tests and the installation integration test.
5. Reinstall from the repository root to deploy the updated binaries, skill metadata, and global reference.

## Install

Run `pwsh -NoProfile -File scripts/install.ps1` from the repository root. The installer builds seven executable names, sets `AGENT_TOOLS_HOME`, installs the skill under `%CODEX_HOME%\skills\agent-tools`, and replaces the global `AGENTS.md` marker block from the validated template.

The installed skill contains:

```text
%USERPROFILE%\.codex\skills\agent-tools\
  SKILL.md
  agents\openai.yaml
  assets\global-agents.md
  scripts\agent-tools.go
  scripts\agent-tools_test.go
```

Routine command syntax, read limits, output boundaries, PowerShell transport, and dependency behavior belong in the global reference, not duplicated here.
