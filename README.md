# CLI Template

A Go CLI scaffold for building tools that serve two audiences: **LLM agents** consume plain-text output naturally (progressive disclosure, drillable references, corrective errors) and **shell scripts** get stable NDJSON via `--json`.

## Motivation

LLM agents and shell scripts have fundamentally different consumption models:

- **LLM agents** read natural language. They reason best over compact, readable plain text with context embedded inline — not JSON. Large output degrades reasoning, so output must be bounded. References should be drillable commands, not raw IDs to construct manually.
- **Shell scripts** need machine-parseable structure. JSON is the right format: typed fields, stable names, no footer noise.

The two-layer model exists because these requirements conflict. Layer 1 returns raw `(string, error)` — pure execution, no I/O policy. Layer 2 applies output shaping once, after execution: overflow truncation, the `[exit:N | Xms]` footer, and stderr routing for plain-text callers; or bypasses everything and emits clean NDJSON for `--json`. Neither audience compromises for the other.

> **Building a CLI with an LLM?** Point it at `ARCHITECTURE.md` — it contains the full design rationale, output contract, and layer rules written as authoritative guidance an agent can follow directly.

## Principles

- **Progressive disclosure.** Output is truncated to ~200 lines; full content lands in `/tmp` with navigation hints. Results include `→ YOUR_CLI <cmd> <ref>` so the agent can drill deeper. Pagination footers are complete runnable commands.
- **Output teaches the agent its own API.** Every result that references a sub-resource hands the agent the exact command to fetch it — no ID construction, no manual reference building.
- **Errors are corrective.** Every error tells the caller what command to run next.
- **Every response carries an exit code and elapsed time.** The footer `[exit:N | Xms]` tells the agent whether the command succeeded and how long it took — useful for diagnosing slow calls and for deciding whether to retry.
- **`--json` is a stability contract.** NDJSON, one object per line, field names/types don't change within a version series. Scripts and programs depend on this.
- **Commands are not interactive.** No spinners, no prompts, no readline. The caller is a loop.
- **macOS Keychain for credentials.** One generic-password item per named profile. No config files, no env vars leaking secrets into process tables.
- **Layer separation is structural, not stylistic.** Layer 1 returns raw `(string, error)`. Layer 2 handles overflow, footer, stderr. Never mix them.

## Quick Start

```bash
# 1. Copy the template
cp -r cli-template ~/new-cli && cd ~/new-cli

# 2. Replace placeholders
find . -type f -name '*.go' -o -name '*.md' | xargs sed -i '' \
  -e 's/YOUR_CLI/mycli/g' \
  -e 's/YOUR_ORG/myorg/g'

# 3. Update go.mod
go mod edit -module github.com/myorg/mycli
go mod tidy

# 4. Verify it builds and runs
go build -o mycli .
./mycli hello --profile test   # (will fail gracefully — no keychain entry)
```

## What You Get

```
main.go                      Entry point, WrapWithPresenter, errAlreadyPresented
cmd/hello.go                 Example command showing all three output modes
internal/keychain/           macOS Keychain CRUD with profile index + default resolution
internal/output/presenter.go Layer 2: overflow, footer, stderr attachment
ARCHITECTURE.md              Design rationale and constraints
CLAUDE.md                    Agent-facing quick reference (command table, output contract)
CODING-INSTRUCTIONS.md       Developer conventions (errors, testing, naming)
.claude/skills/update-docs   Skill for keeping docs in sync with source
```

## What You Replace

| Placeholder | Meaning | Example |
|---|---|---|
| `YOUR_CLI` | Binary name, used in help text and overflow hints | `mycli` |
| `YOUR_ORG` | GitHub org in module path | `myorg` |
| `serviceName` in `keychain.go` | Keychain service label | `"mycli"` |

## Adding a Command

1. Create `cmd/foo.go` with a `FooFlags` struct and a `func Foo(flags FooFlags) (string, error)`.
2. Create `NewFooCmd() *cobra.Command` that wires flags → struct → `Foo()` → `c.OutOrStdout()`.
3. In `main.go`'s `buildRoot`, register: `WrapWithPresenter(cmd.NewFooCmd(), stdout, stderr)`.
4. If results reference sub-resources, include `→ YOUR_CLI <cmd> <ref>` drill-down lines.
5. If paginated, emit a plain-text footer with the full next-page command reconstructed.
6. Add `--json` if the command produces structured records.
7. Write tests against the pure `Foo()` function — no subprocess, no network.

## Architecture at a Glance

```
┌─────────────────────────────────────────────┐
│  Layer 2: Presentation                      │  overflow | footer | stderr
├─────────────────────────────────────────────┤
│  Layer 1: Execution                         │  cmd/ → internal/ → API
└─────────────────────────────────────────────┘
```

Layer 1 returns `(string, error)`. Layer 2 is applied once, after execution, in `main.go`. See `ARCHITECTURE.md` for the full rationale.

## Output Modes

| Mode | Flag | Audience | Layer 2 |
|---|---|---|---|
| Plain text | (default) | LLM agents — natural language, bounded, self-navigating | Applied: overflow, footer, stderr |
| NDJSON | `--json` | Shell scripts, programs — stable typed fields, no footer noise | Bypassed entirely |
| Pretty | `--pretty` | Human terminals — ANSI colour, relative timestamps | Applied: footer + overflow |

## Streaming Commands

Commands that write output as it arrives (WebSocket, long-poll) cannot use `WrapWithPresenter`. They:
1. Write events directly to `stdout`.
2. Apply the footer once at exit via `output.Format`.
3. Return `errAlreadyPresented`.

---

Heavily inspired by [this Reddit post](https://www.reddit.com/r/LocalLLaMA/comments/1rrisqn/i_was_backend_lead_at_manus_after_building_agents) by u/MorroHsu.
