// Package output implements Layer 2 of the two-layer architecture: the
// presentation layer that transforms raw command results into a form safe and
// efficient for LLM consumption.
//
// Layer 1 (execution) produces raw output as (string, error). Layer 2 is
// applied after execution completes and never touches execution logic — so
// pipes and composed calls are unaffected.
//
// The three mechanisms:
//   - Overflow: truncate large output and write the full content to /tmp,
//     returning a path hint the agent can explore with grep/tail.
//   - Metadata footer: append [exit:N | Xms] to every response.
//   - stderr attachment: always attach stderr on non-zero exit.
package output

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	// overflowLineLimit is the maximum number of lines returned directly.
	overflowLineLimit = 200
	// overflowByteLimit is the maximum byte size returned directly.
	overflowByteLimit = 50 * 1024 // 50 KB
)

// toolName is the CLI binary name used in overflow hints. Set this to your
// binary name once at startup, or change the constant directly.
var toolName = "cli"

// SetToolName sets the binary name embedded in overflow navigation hints.
func SetToolName(name string) { toolName = name }

// overflowCounter ensures unique temp file names within a process lifetime.
var overflowCounter atomic.Int64

// Result holds a completed command result ready for presentation.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Elapsed  time.Duration
}

// Format applies all Layer 2 mechanisms and returns the final string to
// present to the caller (LLM agent or terminal).
func Format(r Result) string {
	var b strings.Builder

	// Overflow: truncate large stdout, write full content to a temp file.
	b.WriteString(overflow(r.Stdout))

	// stderr attachment: always include on failure — callers need to know why.
	if r.ExitCode != 0 && strings.TrimSpace(r.Stderr) != "" {
		if !strings.HasSuffix(b.String(), "\n") {
			b.WriteByte('\n')
		}
		b.WriteString("[stderr] ")
		b.WriteString(strings.TrimRight(r.Stderr, "\n"))
		b.WriteByte('\n')
	}

	// Metadata footer: exit code + duration, always present.
	fmt.Fprintf(&b, "[exit:%d | %s]\n", r.ExitCode, formatDuration(r.Elapsed))

	return b.String()
}

// Print writes the formatted result: stdout (with footer) to stdout, and
// stderr content (when non-empty and exit != 0) to the real stderr writer.
func Print(stdout, stderr io.Writer, r Result) {
	fmt.Fprint(stdout, Format(r))
	if r.ExitCode != 0 && strings.TrimSpace(r.Stderr) != "" {
		fmt.Fprintln(stderr, strings.TrimRight(r.Stderr, "\n"))
	}
}

// overflow checks whether stdout exceeds the line or byte limits. If it does,
// the full content is written to a temp file and the returned string contains
// only the first overflowLineLimit lines plus an overflow notice. Otherwise
// stdout is returned unchanged.
func overflow(stdout string) string {
	lines := strings.Split(stdout, "\n")
	// Remove the trailing empty element that results from a final newline.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	tooManyLines := len(lines) > overflowLineLimit
	tooBig := utf8.RuneCountInString(stdout) > overflowByteLimit

	if !tooManyLines && !tooBig {
		return stdout
	}

	tmpPath := writeTempFile(stdout)

	truncated := lines
	if len(lines) > overflowLineLimit {
		truncated = lines[:overflowLineLimit]
	}

	var b strings.Builder
	b.WriteString(strings.Join(truncated, "\n"))
	b.WriteByte('\n')
	fmt.Fprintf(&b, "\n--- output truncated (%d lines, %s) ---\n", len(lines), formatBytes(len(stdout)))
	if tmpPath != "" {
		fmt.Fprintf(&b, "Full output: %s\n", tmpPath)
		fmt.Fprintf(&b, "Explore:     cat %s | grep <pattern>\n", tmpPath)
		fmt.Fprintf(&b, "             cat %s | tail 100\n", tmpPath)
		fmt.Fprintf(&b, "Narrow:      %s <command> --help\n", toolName)
	}
	return b.String()
}

// writeTempFile writes content to a uniquely named file under /tmp/<toolName>-output/
// and returns the path. Returns empty string on error.
func writeTempFile(content string) string {
	dir := filepath.Join(os.TempDir(), toolName+"-output")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	n := overflowCounter.Add(1)
	path := filepath.Join(dir, fmt.Sprintf("output-%d.txt", n))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return ""
	}
	return path
}

// formatDuration formats elapsed time compactly.
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	default:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
}

// formatBytes formats a byte count as a human-readable string.
func formatBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	}
}
