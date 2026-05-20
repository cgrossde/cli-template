Before planning any feature, read `ARCHITECTURE.md`.
Before writing any code, read `CODING-INSTRUCTIONS.md`.

# YOUR_CLI — Agent Coding Instructions

## Project purpose

`YOUR_CLI` is a Unix CLI tool that gives an AI agent programmatic access to
[describe the service]. The intended caller is an LLM agent. Every design
decision must serve that caller first.

---

## Commands

| Command | Flags | Description |
|---|---|---|
| `auth login` | `--profile` (required) | Authenticate and save credentials to Keychain |
| `auth logout` | `--profile` (required) | Remove Keychain entry |
| `auth status` | `--profile` (optional) | Verify saved credentials |
| `auth default` | `--profile` (optional) | Get or set the default profile |
| `hello` | `--profile`, `-n/--count`, `--json`, `--pretty` | Greet the authenticated profile (example command) |

Full usage details: `docs/` directory.

---

## Output contract

### Default (plain text)

Every command writes to stdout with a trailing footer:

```
[output]
[exit:0 | 12ms]
```

On failure, stderr is always included:

```
[stdout if any]
[stderr] reason here
[exit:1 | 3ms]
```

On error, help is emitted before the error block — no-arg or bad-arg invocations
are always self-documenting:

```
Usage:
  YOUR_CLI hello [flags]

Flags:
  ...

[stderr] credentials not found for profile "prod" — run: YOUR_CLI auth login
[exit:1 | 2ms]
```

### Overflow (progressive disclosure)

Output exceeding **~200 lines or ~50 KB** is automatically truncated. The full
content is written to `/tmp/YOUR_CLI-output/output-N.txt` and a navigation
block is appended:

```
[first 200 lines]

--- output truncated (1420 lines, 89.4KB) ---
Full output: /tmp/YOUR_CLI-output/output-3.txt
Explore:     cat /tmp/YOUR_CLI-output/output-3.txt | grep <pattern>
             cat /tmp/YOUR_CLI-output/output-3.txt | tail 100
Narrow:      YOUR_CLI <command> --help
```

The full data is never lost. The agent uses `grep`, `tail`, `head` to navigate,
or re-runs the command with narrower flags to reduce output at the source.

### Self-navigating output

Every result that references a sub-resource includes a ready-to-run drill-down
command. The agent never constructs references manually:

```
  [message content]
    → YOUR_CLI read C012ABC3:1718197800.000100
```

### Plain-text pagination

Paginated commands emit a footer with the complete next-page command,
all flags reconstructed — copy-paste to continue:

```
--- page 1 of 3 | next: YOUR_CLI search --page 2 --count 20 --channel general "deploy" ---
```

No trailer when on the last page.

### JSON mode (`--json`)

Commands with structured output support `--json`. When set:

- Output is **NDJSON** — one JSON object per line, no envelope, no top-level array.
- **The presenter is bypassed entirely.** No `[exit:N | Xms]` footer, no overflow, no stderr attachment.
- Errors are written to **stderr only** as plain text; stdout may be empty or partial.
- `--json` field names and types are a stability contract. Adding new fields is
  allowed. Removing, renaming, or changing a field's type is a breaking change.

**Pagination:** When more results exist, the final line of stdout is a trailer:
```json
{"_pagination": {"next_page": 2, "has_more": true, "total": 47, "page": 1, "pages": 3}}
```
- Detect with: the line starts with `{"_pagination"` (or `jq 'select(._pagination)'`)
- Fetch next page: pass `--page <next_page>` (page-based) or `--cursor <next_cursor>` (cursor-based)
- **No trailer on the last page.** Absence of the trailer means "done."
- `has_more` is always a boolean — the canonical "keep paging?" signal.

### Pretty mode (`--pretty`)

Selected commands support `--pretty` for human-readable terminal output with
ANSI colours, bold text, and relative timestamps. This mode is not machine-
parseable and has no output stability guarantee. Layer 2 applies normally —
the `[exit:N | Xms]` footer is present and overflow fires on large output.

---

## Architecture

Two-layer model — see `ARCHITECTURE.md` for the full design rationale.

- **Layer 1** (`cmd/`, `internal/`): executes, returns `(string, error)`, no truncation or annotation
- **Layer 2** (`internal/output/`, wired in `main.go`): overflow, footer, stderr attachment

---

## Adding a new command

Checklist before a command is considered complete:

- [ ] Exits 0 on success, non-zero on all failure paths
- [ ] Writes only parseable text to stdout
- [ ] All errors include corrective guidance (`run: YOUR_CLI auth login`)
- [ ] `[exit:N | Xms]` footer on every response
- [ ] `--help` implemented with complete flag documentation
- [ ] No-arg invocation prints help then the error (WrapWithPresenter handles this automatically)
- [ ] Overflow mode applied if output can be large (automatic)
- [ ] Long description in `Long` field; concrete examples in `Example` field
- [ ] Tests using real Keychain save and restore prior state in `t.Cleanup`
- [ ] Layer 1 function (`cmd/`) returns `(string, error)` — no direct I/O
- [ ] `WrapWithPresenter` called in `main.go`'s `buildRoot`
- [ ] `--profile` flag is **optional** on every command; resolve via `keychain.ResolveDefault()` when empty

If the command has structured output, also add `--json`:

- [ ] `--json` flag registered on the command (not globally)
- [ ] Layer 1 returns NDJSON string when `flags.JSON` is true — one object per line, no footer
- [ ] Errors in JSON mode: written to `cmd.ErrOrStderr()`, nothing to stdout, return `errAlreadyPresented`
- [ ] Paginated commands: emit `{"_pagination": {...}}` trailer when more pages exist
- [ ] `WrapWithPresenter` bypass is automatic when `--json` is registered — no extra wiring
- [ ] Tests for the JSON formatter (field names, pagination trailer)

If the command has terminal-facing output, also add `--pretty`:

- [ ] `--pretty` flag registered on the command
- [ ] Layer 1 returns ANSI-formatted string when `flags.Pretty` is true
- [ ] Footer is present (Layer 2 applies normally; no special wiring needed)
- [ ] `--pretty` output is not subject to the stability contract

### Presenter patterns

Two patterns exist; choose based on whether the command streams output:

**`WrapWithPresenter`** (most commands): command writes to `c.OutOrStdout()`,
presenter captures it and emits the footer. Gives help-on-error automatically.
Wire in `buildRoot`.

**Inline presenter** (streaming commands): `RunE` is built in `main.go`, writes
directly to `stdout`, emits footer once at exit via `output.Format`. No
automatic help-on-error. Return `errAlreadyPresented` at the end.

---

## Reference

- `ARCHITECTURE.md` — two-layer model, output modes, design constraints
- `CODING-INSTRUCTIONS.md` — Go style, error handling, testing rules
- `docs/` — per-command reference docs
