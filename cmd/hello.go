// Package cmd — hello.go implements the "hello" command.
//
// This is the template example command. It demonstrates the canonical Layer 1
// pattern: parse flags into a typed struct, call a pure function that returns
// (string, error), write to c.OutOrStdout(). Layer 2 wiring (presenter) is
// applied in main.go via WrapWithPresenter.
package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/YOUR_ORG/YOUR_CLI/internal/keychain"
)

// HelloFlags holds the parsed flag values for the hello command.
type HelloFlags struct {
	Profile string
	Count   int
	JSON    bool
	Pretty  bool
}

// helloRecord is the JSON schema for one hello record.
type helloRecord struct {
	Profile  string `json:"profile"`
	Greeting string `json:"greeting"`
	Index    int    `json:"index"`
}

// NewHelloCmd builds the "hello" Cobra command.
func NewHelloCmd() *cobra.Command {
	var flags HelloFlags

	c := &cobra.Command{
		Use:   "hello",
		Short: "Greet the authenticated profile",
		Long: `Greet the authenticated profile by name.

Loads credentials from the Keychain for the resolved profile and prints a
greeting. Demonstrates the standard command pattern: flag struct, Layer 1
function returning (string, error), optional --json and --pretty modes.`,
		Example: `  # Default profile
  YOUR_CLI hello

  # Explicit profile
  YOUR_CLI hello --profile staging

  # Machine-readable output (NDJSON, one object per line)
  YOUR_CLI hello --json

  # Human-readable ANSI output (terminal only)
  YOUR_CLI hello --pretty`,
		RunE: func(c *cobra.Command, args []string) error {
			out, err := Hello(flags)
			if err != nil {
				return err
			}
			fmt.Fprint(c.OutOrStdout(), out)
			return nil
		},
	}

	c.Flags().StringVarP(&flags.Profile, "profile", "p", "", "Profile name (default: resolved from Keychain)")
	c.Flags().IntVarP(&flags.Count, "count", "n", 1, "Number of greetings to emit")
	c.Flags().BoolVar(&flags.JSON, "json", false, "Output NDJSON — one object per line, no footer")
	c.Flags().BoolVar(&flags.Pretty, "pretty", false, "ANSI-styled output for terminal display")
	return c
}

// Hello is the Layer 1 implementation. It resolves the profile, loads
// credentials from the Keychain, and formats the output.
//
// Returns NDJSON when flags.JSON is true. Returns ANSI-styled text when
// flags.Pretty is true. Returns plain text otherwise.
func Hello(flags HelloFlags) (string, error) {
	profile := flags.Profile
	if profile == "" {
		var err error
		profile, err = keychain.ResolveDefault()
		if err != nil {
			return "", fmt.Errorf("%w\nRun: YOUR_CLI auth login", err)
		}
	}

	entry, err := keychain.Load(profile)
	if err != nil {
		return "", fmt.Errorf("credentials not found for profile %q — run: YOUR_CLI auth login\ndetail: %w", profile, err)
	}

	n := flags.Count
	if n < 1 {
		n = 1
	}

	if flags.JSON {
		return helloJSON(entry, n), nil
	}
	if flags.Pretty {
		return helloPretty(entry, n), nil
	}
	return helloPlain(entry, n), nil
}

func helloPlain(entry keychain.Entry, n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "Hello, %s! (greeting %d of %d)\n", entry.Profile, i+1, n)
	}
	return b.String()
}

func helloPretty(entry keychain.Entry, n int) string {
	// Replace with your preferred ANSI rendering. This is a minimal example
	// using ANSI escape codes directly; consider lipgloss for more complex UIs.
	const bold = "\033[1m"
	const reset = "\033[0m"
	const blue = "\033[34m"

	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "%s●%s %sHello, %s!%s  (greeting %d of %d)\n",
			blue, reset, bold, entry.Profile, reset, i+1, n)
	}
	return b.String()
}

func helloJSON(entry keychain.Entry, n int) string {
	var b strings.Builder
	for i := range n {
		rec := helloRecord{
			Profile:  entry.Profile,
			Greeting: fmt.Sprintf("Hello, %s!", entry.Profile),
			Index:    i + 1,
		}
		line, _ := json.Marshal(rec)
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.String()
}
