package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/YOUR_ORG/YOUR_CLI/cmd"
	"github.com/YOUR_ORG/YOUR_CLI/internal/output"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		os.Exit(1)
	}
}

// run is the testable entry point. stdout receives presenter-formatted output;
// stderr receives progress messages and slog output.
//
// Any error returned by Cobra (flag errors, unknown commands) is formatted
// through the presenter so the caller always sees a [exit:N | Xms] footer.
//
// errAlreadyPresented is returned by RunE implementations that have already
// written formatted output themselves (e.g. streaming commands). run()
// recognises it and exits non-zero without emitting a second presenter block.
var errAlreadyPresented = errors.New("already presented")

func run(args []string, stdout, stderr io.Writer) error {
	slog.SetDefault(slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	output.SetToolName("YOUR_CLI")

	start := time.Now()
	root := buildRoot(stdout, stderr)
	root.SetArgs(args)
	err := root.Execute()
	if err == nil {
		return nil
	}
	if errors.Is(err, errAlreadyPresented) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return err
	}

	// All other errors (missing flags, unknown commands, etc.) go through
	// the presenter so output is always structured.
	usageStr := ""
	if found, _, findErr := root.Find(args); findErr == nil && found != nil {
		usageStr = found.UsageString()
	}
	output.Print(stdout, stderr, output.Result{
		Stdout:   usageStr,
		Stderr:   err.Error(),
		ExitCode: 1,
		Elapsed:  time.Since(start),
	})
	return err
}

// buildRoot constructs the full Cobra command tree and returns the root command.
// stdout/stderr are injected so every command's output is testable.
func buildRoot(stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "YOUR_CLI",
		Short:         "One-line description of what this CLI does",
		SilenceUsage:  true, // don't dump usage on every RunE error; presenter does it
		SilenceErrors: true, // we handle error printing via the presenter
	}
	root.SetOut(stdout)
	root.SetErr(stderr)

	// Help template: Usage+Flags first, then Long description.
	// Cobra's default puts Long first, which buries flags for a machine caller.
	root.SetHelpTemplate(
		"{{if or .Runnable .HasSubCommands}}{{.UsageString}}{{end}}" +
			"{{with (or .Long .Short)}}{{if not (or $.Runnable $.HasSubCommands)}}" +
			"{{. | trimTrailingWhitespaces}}\n\n{{else}}\n{{. | trimTrailingWhitespaces}}\n" +
			"{{end}}{{end}}")

	// Register commands. Call WrapWithPresenter on every leaf command that
	// should have the footer. Streaming commands apply the presenter inline.
	helloCmd := cmd.NewHelloCmd()
	WrapWithPresenter(helloCmd, stdout, stderr)
	root.AddCommand(helloCmd)

	// Add more commands here:
	// fooCmd := cmd.NewFooCmd()
	// WrapWithPresenter(fooCmd, stdout, stderr)

	return root
}

// WrapWithPresenter wraps a *cobra.Command's RunE so its output passes through
// the Layer 2 presenter.
//
// The flow:
//  1. cmd.OutOrStdout() is redirected to an in-memory buffer.
//  2. RunE executes and writes raw output to that buffer.
//  3. On return, the buffer contents are passed to output.Format together with
//     elapsed time, exit code, and any error string.
//  4. The formatted result (including the [exit:N | Xms] footer) is written to
//     finalOut.
//
// JSON bypass: when the command has a --json flag and it is set, the buffer is
// written verbatim to finalOut without any formatting. The footer is suppressed
// because it would corrupt the NDJSON stream. Errors go to stderr only.
//
// Help bypass: Cobra's --help writes to cmd.OutOrStdout(). Since we redirect
// that to a buffer, help would be swallowed. We override HelpFunc to write
// directly to finalOut so --help always reaches the caller.
//
// Error path: when RunE returns a non-nil error, help is emitted first, then
// a blank line, then the [stderr] error and the [exit:1] footer. This means
// no-arg or bad-arg invocations are always self-documenting.
func WrapWithPresenter(c *cobra.Command, finalOut io.Writer, finalErr io.Writer) {
	original := c.RunE
	if original == nil {
		return
	}
	var buf bytes.Buffer
	c.SetOut(&buf)

	defaultHelp := c.HelpFunc()
	c.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		cmd.SetOut(finalOut)
		defaultHelp(cmd, args)
		cmd.SetOut(&buf)
	})

	c.RunE = func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		err := original(cmd, args)
		elapsed := time.Since(start)

		// JSON mode: bypass presenter — footer corrupts the NDJSON stream.
		if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
			if buf.Len() > 0 {
				fmt.Fprint(finalOut, buf.String())
			}
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), err.Error())
				return errAlreadyPresented
			}
			return nil
		}

		exitCode := 0
		stderrStr := ""
		if err != nil {
			exitCode = 1
			stderrStr = err.Error()
			// Emit help before the error block so the caller knows what to supply.
			cmd.HelpFunc()(cmd, args)
			fmt.Fprintln(finalOut)
		}

		output.Print(finalOut, finalErr, output.Result{
			Stdout:   buf.String(),
			Stderr:   stderrStr,
			ExitCode: exitCode,
			Elapsed:  elapsed,
		})

		if err != nil {
			return errAlreadyPresented
		}
		return nil
	}
}
