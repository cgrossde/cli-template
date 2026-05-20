# CLI Architecture

## Overview

This CLI is built around two strictly separated layers. The boundary between them is a logical necessity, not a style choice.

```
┌─────────────────────────────────────────────┐
│  Layer 2: Presentation Layer                │  ← Serves LLM + terminal constraints
│  Overflow | Metadata footer | stderr attach  │
├─────────────────────────────────────────────┤
│  Layer 1: Execution Layer                   │  ← Pure command semantics
│  Command routing | API calls | raw output   │
└─────────────────────────────────────────────┘
```

---

## Why Two Layers

This CLI serves two primary callers:

1. **LLM agents** — need progressive disclosure (overflow), structural signals (footer), and self-navigating output (drillable refs, pagination-as-commands).
2. **Scripts and programs** — need stable, parseable structured output (`--json` NDJSON mode).

Both share a constraint: **Layer 1 must produce raw, lossless output.** If you truncate a response mid-processing, you break composition. If you annotate it with footers inside execution code, you corrupt the JSON stream. The only correct position for presentation transforms is **after** execution completes.

The LLM caller drives the need for Layer 2 (overflow, footer, stderr attachment). The script caller drives the need for `--json` to bypass Layer 2 entirely. Both depend on Layer 1 being pure.

The `--pretty` mode (human-readable ANSI output) is a third caller type. It also belongs in Layer 2 — or as a rendering path inside Layer 1 that returns an ANSI string — never as logic mixed into the API call itself.

---

## Layer 1: Execution

**Responsibility:** Call the API. Return raw results.

- Routes subcommands to API calls
- Handles authentication (loads credentials from Keychain)
- Captures full API responses — no truncation, no annotation
- Returns raw output as `(string, error)`

Layer 1 has no knowledge of output limits, terminal width, or ANSI codes (except for `--pretty` rendering paths that return formatted strings). It executes and returns.

**Files:**
- `internal/` — API client packages
- `cmd/` — subcommand routing and flag parsing

---

## Layer 2: Presentation

**Responsibility:** Transform Layer 1 output for safe, efficient consumption.

Applied after execution completes. Never touches execution logic.

### Mechanism A: Overflow (Progressive Disclosure)

This is the core mechanism that makes LLM consumption safe. Without it, a single large API response can blow out the agent's context window and degrade all subsequent reasoning.

**Trigger:** output exceeds 200 lines OR 50 KB (whichever fires first).

**Behaviour:**

1. Truncate to the first 200 lines (rune-safe split — no broken UTF-8 mid-character)
2. Write the **complete, unmodified** output to `/tmp/YOUR_CLI-output/output-N.txt`
3. Append an overflow notice with ready-to-run exploration commands

```
[first 200 lines of output, verbatim]

--- output truncated (1420 lines, 89.4KB) ---
Full output: /tmp/YOUR_CLI-output/output-3.txt
Explore:     cat /tmp/YOUR_CLI-output/output-3.txt | grep <pattern>
             cat /tmp/YOUR_CLI-output/output-3.txt | tail 100
Narrow:      YOUR_CLI <command> --help
```

**Why this works:** The agent already knows `grep`, `head`, `tail`, `wc`. Overflow converts a context-budget problem into a navigation skill the agent already has. The full data is never lost — it's one `cat` away. The agent can:
- `grep` for a keyword to find relevant lines
- `tail` to see the end of the output
- `head -n 50` a section after finding a line number
- Re-run the command with narrower flags to reduce output at the source

**Implementation:** `internal/output/presenter.go` — the `overflow()` function. The temp directory uses the tool name (`/tmp/YOUR_CLI-output/`) and a monotonically increasing counter for unique file names within a process lifetime. Files are world-readable (`0600`) and persist until system reboot or explicit cleanup.

**Not applied in JSON mode.** When `--json` is set, overflow is bypassed entirely. Scripts handle their own pagination and memory management.

### Mechanism B: Metadata Footer

After execution, append to every response:

```
[exit:0 | 1.2s]
```

- Exit code using Unix convention (0 = success, non-zero = failure)
- Duration in human-readable form

The footer is **always present**, including on success. The agent internalises these signals over a conversation. Inconsistent output format means every call feels like the first.

The footer is suppressed in JSON mode (`--json`) because it would corrupt the NDJSON stream.

### Mechanism C: stderr Attachment

On any non-zero exit:

```
[stdout content if any]
[stderr] reason for failure here
[exit:1 | 3ms]
```

**Never drop stderr.** The most common mistake is discarding stderr when stdout has content. This is catastrophically wrong for agents: the agent receives "it failed" with no information about why, and retries blindly.

Errors must be self-contained and corrective. Include the exact command the agent should run to recover:

```
[stderr] credentials not found for profile "prod" — run: YOUR_CLI auth login
[exit:1 | 2ms]
```

### Mechanism D: Help on Error

When a wrapped command's `RunE` returns a non-nil error, `WrapWithPresenter` emits the command's help text before the error block:

```
Usage:
  YOUR_CLI hello [flags]

Flags:
  --profile string   Profile name
  --json             Output NDJSON
  ...

[stderr] credentials not found for profile "prod" — run: YOUR_CLI auth login
[exit:1 | 2ms]
```

No-arg or bad-arg invocations are always self-documenting. The caller never needs to separately invoke `--help` to understand what went wrong.

---

## Output Modes

Every command that produces structured records should support three output modes. Choose via flags:

| Mode | Flag | Format | Audience |
|---|---|---|---|
| Plain text | (default) | Human-readable text + `[exit:N]` footer | LLM agents |
| NDJSON | `--json` | One JSON object per line, no footer | Scripts, programs |
| Pretty | `--pretty` | ANSI-styled text + `[exit:N]` footer | Human terminal users |

The three modes are mutually exclusive. `--json` takes precedence; `--pretty` is ignored when `--json` is set.

### Plain Text (default)

The primary output format. Designed for LLM agents: parseable with `grep`, `awk`, `head`. The footer is always present. Overflow is applied automatically.

### JSON Mode (`--json`)

**Layer 2 is bypassed entirely.** When `--json` is set, `WrapWithPresenter` writes the raw buffer directly to stdout without footer, overflow, or stderr attachment. The footer would corrupt the NDJSON stream.

Rules:

- **One JSON object per line.** No top-level array, no envelope. Each logical record is emitted as a single compact JSON object followed by a newline (NDJSON convention). `wc -l` counts records; `grep` filters them; `jq -c '.'` validates.
- **Errors go to stderr only, exit non-zero.** In JSON mode, error messages are written to stderr as plain text. stdout may be empty or contain partial NDJSON if an error occurs mid-stream. No JSON error object is written to stdout — the stream must remain parseable.
- **No auto-pagination.** Callers must page explicitly. There is no `--all` flag that silently fetches all pages — the caller controls its own memory budget.
- **Output stability is a contract.** Within a version series, `--json` field names and types are stable. Adding new fields is allowed (callers must tolerate unknown keys). Removing, renaming, or changing a field's type is a breaking change.

#### JSON Pagination

When a command supports pagination and more results exist, the **last line** of stdout is a pagination trailer object:

```json
{"_pagination": {"next_page": 2, "has_more": true, "total": 47, "page": 1, "pages": 3}}
```

Contract:

- The trailer is always the final line. Data records precede it.
- The leading underscore on `_pagination` makes it unambiguously not a data record. Consumers detect it with `jq 'select(._pagination)'` or a prefix check.
- **No trailer is emitted on the last page.** If the trailer is absent, there are no more results.
- The trailer fields depend on the pagination style:

| Style | Fields | How to fetch next page |
|---|---|---|
| Page-based | `next_page`, `has_more`, `total`, `page`, `pages` | Pass `--page <next_page>` |
| Cursor-based | `has_more`, `next_cursor` | Pass `--cursor <next_cursor>` |

- Both styles are valid. Choose based on what the upstream API provides.
- `has_more` is always present and always a boolean. It's the canonical "should I keep paging?" signal.

Example consumer (shell):
```bash
# Fetch all pages into a single file, stripping pagination trailers
page=1
while true; do
  YOUR_CLI search --json --page "$page" "query" > /tmp/page.json
  grep -v '^{"_pagination"' /tmp/page.json >> results.json
  next=$(jq -r 'select(._pagination) | ._pagination.next_page // empty' /tmp/page.json)
  [ -z "$next" ] && break
  page="$next"
done
```

Example consumer (Go):
```go
// Detect trailer: last line starts with {"_pagination"
lines := strings.Split(strings.TrimSpace(stdout), "\n")
last := lines[len(lines)-1]
if strings.HasPrefix(last, `{"_pagination"`) {
    var trailer struct { P struct { NextPage int `json:"next_page"` } `json:"_pagination"` }
    json.Unmarshal([]byte(last), &trailer)
    // trailer.P.NextPage is the next --page value
}
```
### Pretty Mode (`--pretty`)

Human-readable, ANSI-styled output for terminal display. Typically uses colour, bold, and relative timestamps. Should detect terminal capability before emitting ANSI codes.

`--pretty` output is not machine-parseable and is not subject to the output stability contract. It can change freely between versions.

Layer 2 applies normally in `--pretty` mode. The `[exit:N | Xms]` footer is present, overflow fires if the rendered output is large, and stderr is attached on failure. The ANSI escape sequences in the output are plain strings — `output.Format` counts runes and appends text without interpreting them.

---

## Authentication and Credentials

Credentials are stored in the macOS Keychain via `internal/keychain`. One generic-password item per named profile.

All commands that require credentials:
1. Read the `--profile` flag (or equivalent)
2. If empty, call `keychain.ResolveDefault()`
3. Call `keychain.Load(profile)` to retrieve the entry
4. Pass the credentials to the API client

The `--profile` flag (or whatever your credential selector is named) is **always optional** on every command. Never use `MarkFlagRequired` on it. Resolve via `keychain.ResolveDefault()` when absent: a single saved profile wins implicitly; multiple saved profiles require an explicit flag.

Resolution order for `ResolveDefault`:
1. Stored default (`keychain.GetDefault`)
2. Single saved profile (implicit)
3. Error — ambiguous or empty

---

## Package Structure

```
YOUR_CLI/
├── main.go                    Entry point: run(args, stdout, stderr); Layer 1→2 bridge
├── main_test.go               Tests for run() and top-level routing
├── cmd/
│   ├── hello.go               Example command (Layer 1: flags, pure function, return string)
│   └── hello_test.go
├── internal/
│   ├── keychain/
│   │   └── keychain.go        macOS Keychain: save/load/delete/list/default credentials
│   └── output/
│       └── presenter.go       Layer 2: overflow, footer, stderr attachment
└── ARCHITECTURE.md
```

### Entry point contract

```go
func main() {
    if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
        os.Exit(1)
    }
}

func run(args []string, stdout, stderr io.Writer) error { … }
```

`run` takes explicit I/O writers. Tests pass `bytes.Buffer`; production passes `os.Stdout`/`os.Stderr`. No I/O is hardcoded below `main()`.

### Layer 1 → Layer 2 bridge

`cmd/` functions return `(string, error)` — raw output string and execution error. `main.go`'s `WrapWithPresenter` captures the output into a buffer, measures elapsed time, and calls `output.Format` before writing to `stdout`.

When `--json` is set on the command, `WrapWithPresenter` bypasses `output.Format` entirely and writes the buffer verbatim — no footer, no overflow.

When `--pretty` is set, the command returns an ANSI-formatted string from Layer 1. `WrapWithPresenter` still applies the footer unless the command is a streaming one that applies the presenter inline.

---

## Cobra Wiring Rules

- Never set `RunE` on a command group. Bare group invocation must show help.
- `SilenceUsage: true` and `SilenceErrors: true` on root. Errors are printed through the presenter, not Cobra's default handler.
- `MarkFlagRequired` must be called on the exact command that owns the flag, not on parent groups.
- A `RunE` that writes its own presenter output (streaming commands) must return `errAlreadyPresented`.
- Never call `os.Exit` inside `RunE`. It makes the code path untestable and violates the `run()` contract.

### Streaming commands

Commands that stream output (WebSocket events, long polls, live tails) cannot use `WrapWithPresenter` because there is no single return point to wrap. They:

1. Write events directly to `stdout` as they arrive
2. Apply the footer once at exit via `output.Format`
3. Return `errAlreadyPresented` so `run()` does not emit a second footer

In JSON mode, streaming commands suppress the footer entirely and write errors to stderr only.

---

## Design Constraints

**Layer 1 must be raw and lossless.** Do not truncate, annotate, or transform output inside execution code. Pass the full result up.

**Layer 2 must not call APIs.** Presentation logic has no business making network calls. If you find yourself needing to fetch additional data in the presenter, it belongs in a Layer 1 command.

**Output must be pipeable.** Every command's stdout must survive `| grep`, `| jq`, `| head`. The metadata footer uses bracket syntax (`[exit:0]`) that is unlikely to appear as data content and can be stripped with `grep -v '^\[exit:'` if needed.

**Commands are not interactive.** No `readline`, no spinners on stdout, no "press enter to continue." The primary caller is a program running in a loop.

**Errors are corrective, not descriptive.** Every error message tells the caller exactly what to run next. "credentials not found" is not an error message. "credentials not found for profile "prod" — run: YOUR_CLI auth login" is.


**Output is self-navigating.** Every result that references a deeper resource includes the exact command to fetch it. The agent never needs to construct references from raw IDs — the output hands them ready-to-run. For example, a search result includes `→ YOUR_CLI read <channel>:<ts>` on each match so the agent can drill into any thread without prior knowledge of the reference format.

**Pagination footers are runnable commands.** In plain-text mode, when more pages exist the footer emits the complete next-page command with all active flags reconstructed. The agent copy-pastes it directly — no flag reconstruction, no manual cursor management:
```
--- page 1 of 3 | next: YOUR_CLI search --page 2 --count 20 --channel general "deploy" ---
```
In JSON mode this is expressed as a `{"_pagination": {...}}` trailer object instead.
**JSON output stability is a versioned contract.** Field names and types do not change within a version series. New fields may be added; callers must tolerate unknown keys.
