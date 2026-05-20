# Go CLI — Coding Instructions

Minimum rules for this codebase. Read before writing any code.

---

## Language & toolchain

- Go 1.23+. Use stdlib-first; add a dependency only when it earns its keep.
- `go.mod` module path matches the repo. Run `go mod tidy` after every dependency change.
- Format with `gofmt` (or `goimports`). No exceptions.
- Vet with `go vet ./...` before committing.

---

## Project layout

```
main.go              // entry point — parse flags, call run(), exit on error
cmd/
  <command>.go       // Layer 1: flag struct + pure function returning (string, error)
  <command>_test.go
internal/
  <feature>/
    <feature>.go
    <feature>_test.go
```

`main.go` is a thin shell:

```go
func main() {
    if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
        os.Exit(1)
    }
}
```

`run` takes explicit I/O writers. Tests pass `bytes.Buffer`; production passes `os.Stdout`/`os.Stderr`. This makes the entire command tree testable without subprocess overhead.

---

## Two-layer architecture

See `ARCHITECTURE.md` for the full design. Short form:

- **Layer 1** (`cmd/`, `internal/`): executes the command, returns `(string, error)`, no truncation, no annotation, no I/O side effects
- **Layer 2** (`internal/output/`, wired in `main.go`): overflow truncation, `[exit:N | Xms]` footer, stderr attachment

The presenter is applied in `main.go` via `WrapWithPresenter`, never inside `cmd/` or `internal/`.

Layer 1 functions write to `c.OutOrStdout()` (injected writer), never to `os.Stdout` directly.

---

## Errors

Wrap with context at every layer boundary. Never swallow.

```go
// add context, preserve type for callers who need errors.Is / errors.As
return fmt.Errorf("load config: %w", err)

// intentionally opaque — use %v when callers must NOT inspect the type
return fmt.Errorf("load config: %v", err)
```

Sentinel errors for outcomes callers must branch on:

```go
var ErrNotFound = errors.New("not found")
// caller: errors.Is(err, ErrNotFound)
```

Never compare errors with `==` through a wrapped chain. Always use `errors.Is` / `errors.As`.

Return early. Don't nest happy-path logic inside `if err == nil` blocks.

**Errors must be corrective.** Every error returned from a `cmd/` function must tell the caller what to do next. The agent cannot open a browser to search for solutions.

```go
// wrong
return fmt.Errorf("credentials not found")

// right
return fmt.Errorf("credentials not found for profile %q — run: YOUR_CLI auth login", profile)
```

---

## Logging

Use `log/slog`. Do not use `log.Printf`, `logrus`, or `zap`.

```go
slog.Info("starting", "version", version)
slog.Error("command failed", "err", err)
```

Key-value pairs only — no `fmt.Sprintf` inside log calls.

Log to **stderr**. Stdout is for program output that other tools may pipe.

`slog` is for progress messages and diagnostics. Layer 1 command output goes through the presenter as a plain string — not through slog.

---

## CLI flags

Use `github.com/spf13/cobra` for all commands. The tool has subcommands.

- Define flags on the local `*cobra.Command`, not globally.
- Set `SilenceUsage: true` and `SilenceErrors: true` on root; handle error printing through the presenter.
- Print usage to stderr on bad input; exit code 2 for usage errors, 1 for runtime errors.
- `--help` and `--version` are always supported.
- Never use `MarkFlagRequired` on `--profile` (or the credential selector flag). Resolve via `keychain.ResolveDefault()` when absent.
- Every flag that a command accepts must have a complete description in its `Usage` string.

---

## Output modes

Every command that produces structured records should support three modes:

| Mode | Flag | Layer 2 | Notes |
|---|---|---|---|
| Plain text | (default) | Applied fully | Primary format for LLM agents |
| NDJSON | `--json` | Bypassed | One object per line; errors to stderr only |
| Pretty | `--pretty` | Applied fully | ANSI output; not machine-parseable |

When `--json` is registered on a command, `WrapWithPresenter` detects it and bypasses `output.Format` automatically. No extra wiring required.

JSON rules:
- One compact JSON object per line (NDJSON). No array wrapper, no envelope.
- Errors written to `cmd.ErrOrStderr()` as plain text. Return `errAlreadyPresented`.
- Pagination trailer `{"_pagination": {...}}` as the final line when more pages exist.
- Field names and types are a stability contract. Adding fields is OK; renaming or removing is breaking.

---

## Naming

- Packages: short, lowercase, no underscores. `config`, `fetch`, `report` — not `configManager`, `utils`, `common`.
- Unexported by default. Export only what callers outside the package genuinely need.
- Acronyms: `userID`, `httpClient`, `parseURL` — not `userId`, `HttpClient`, `parseUrl`.
- Error variables: `ErrFoo`; error types: `FooError`.
- Flag struct type: `<Command>Flags` (e.g. `HelloFlags`). One per command.

---

## Testing

Every non-trivial function has a `_test.go` file alongside it.

```go
func TestHello_missingProfile(t *testing.T) {
    // keychain has no entries; expect a corrective error
    _, err := Hello(HelloFlags{Profile: "nonexistent"})
    if !errors.Is(err, keychain.ErrNotFound) {
        t.Fatalf("expected ErrNotFound, got %v", err)
    }
}
```

- `testing` + `testify/assert` if needed, nothing else.
- No mocks for filesystem or subprocess — use real temp dirs (`t.TempDir()`) and real binaries.
- Table-driven tests for functions with multiple input variants.
- `t.Fatal` / `t.Errorf` — not `panic`.
- Tests using the real Keychain (Save, SetDefault, etc.) save and restore prior state in `t.Cleanup`.
- Test `run()` end-to-end with `bytes.Buffer` for stdout/stderr — this is the integration seam.

### Interface injection for testability

When a `cmd/` function calls a client type but must be unit-testable without network access, define a minimal unexported interface over only the methods actually called:

```go
type helloClient interface {
    Greet(profile string) (string, error)
}

func Hello(client helloClient, flags HelloFlags) (string, error) { … }
```

The real concrete client satisfies the interface implicitly. Tests provide a hand-written struct literal stub. No mock framework, no generated code.

---

## I/O and stdlib hygiene

- `io.ReadAll`, `os.ReadFile`, `os.WriteFile` — never `ioutil.*` (deprecated since Go 1.16).
- Accept `io.Reader` / `io.Writer` in functions that do I/O; do not hardcode `os.Stdin` / `os.Stdout` below `main`.
- Close resources with `defer` immediately after the open succeeds.
- Check errors from `Close()` on writable resources (files, network connections).

---

## What to avoid

| Don't | Do instead |
|---|---|
| `panic` for recoverable errors | return `error` |
| `init()` with side effects | explicit initialisation in `run()` |
| Global mutable state | pass dependencies explicitly |
| `interface{}` / `any` without cause | typed parameters |
| `ioutil.*` | `io.*` / `os.*` |
| `log.Printf` | `slog.Info` / `slog.Error` |
| `pkg/errors` | `fmt.Errorf("%w", err)` |
| Exported symbol in `internal/` "just in case" | export only at the point of need |
| `os.Exit` inside `RunE` | return `error` or `errAlreadyPresented` |
| Descriptive errors without corrective guidance | "credentials not found — run: YOUR_CLI auth login" |
| `MarkFlagRequired("profile")` | `keychain.ResolveDefault()` |

---

## Adding a new command

Checklist before a command is considered complete:

- [ ] Exits 0 on success, non-zero on all failure paths
- [ ] Writes only to `c.OutOrStdout()`, never `os.Stdout`
- [ ] All errors include corrective guidance
- [ ] `[exit:N | Xms]` footer on every response (handled by `WrapWithPresenter`)
- [ ] `--help` implemented with complete flag documentation
- [ ] No-arg invocation prints help then the error (handled by `WrapWithPresenter`)
- [ ] Overflow mode applied if output can be large (handled automatically)
- [ ] `Long` field used for extended description; `Example` field for concrete examples
- [ ] Tests using real Keychain save and restore prior state in `t.Cleanup`
- [ ] Layer 1 function returns `(string, error)` — no direct I/O
- [ ] `WrapWithPresenter` called in `main.go`'s `buildRoot`
- [ ] `--profile` flag is optional; resolve via `keychain.ResolveDefault()` when empty
- [ ] Results referencing sub-resources include `→ YOUR_CLI <cmd> <ref>` drill-down hints
- [ ] Paginated commands: plain-text footer emits a complete next-page command with all active flags reconstructed

If the command has structured output (records, not prose), also add `--json`:

- [ ] `--json` flag registered on the command
- [ ] Layer 1 returns NDJSON string when `flags.JSON` is true — one object per line, no footer
- [ ] Errors in JSON mode: written to `cmd.ErrOrStderr()`, return `errAlreadyPresented`
- [ ] Paginated commands: emit `{"_pagination": {...}}` trailer when more pages exist
- [ ] `WrapWithPresenter` bypass is automatic when `--json` is registered — no extra wiring
- [ ] Tests for the JSON formatter: field names, pagination trailer

If the command has human-readable terminal output, also add `--pretty`:

- [ ] `--pretty` flag registered on the command
- [ ] Layer 1 returns ANSI-formatted string when `flags.Pretty` is true
- [ ] ANSI output is not subject to the output stability contract
- [ ] Footer is present (Layer 2 applies normally; no special wiring needed)
