// Package cli assembles the cobra command tree.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"webhook-gateway-cli/internal/build"
)

// NewRootCmd builds the command tree. Subcommands are added here so main stays
// a single Execute call.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   build.Name,
		Short: "Command-line client for the webhook gateway",
		Long: fmt.Sprintf(
			"%s talks to a webhook gateway: log in once, then tunnel, replay, and\n"+
				"trigger events from your terminal.", build.Name),
		SilenceUsage:  true, // a runtime failure is not a usage error
		SilenceErrors: true, // Execute prints the error itself, once
		Version:       build.Version,
	}
	root.AddCommand(newLoginCmd(), newListenCmd(), newVersionCmd())
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", build.Name, err)
		return 1
	}
	return 0
}
