package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"webhook-gateway-cli/internal/build"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s/%s\n",
				build.Name, build.Version, runtime.GOOS, runtime.GOARCH)
			return err
		},
	}
}
