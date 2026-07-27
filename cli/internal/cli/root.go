// Package cli assembles the cobra command tree.
package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"webhook-gateway-cli/internal/build"
	"webhook-gateway-cli/internal/client"
	"webhook-gateway-cli/internal/config"
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
	root.AddCommand(newLoginCmd(), newListenCmd(), newReplayCmd(), newTriggerCmd(), newVersionCmd())
	return root
}

// loadCredentials reads the stored gateway credentials, turning the
// "no config yet" case into an instruction rather than a file-not-found error.
func loadCredentials() (config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		if errors.Is(err, config.ErrNotLoggedIn) {
			return config.Config{}, fmt.Errorf("not logged in — run `%s login` first", build.Name)
		}
		return config.Config{}, err
	}
	return cfg, nil
}

// gatewayClient builds an API client from the stored credentials. Commands that
// only speak REST use this; `listen` needs the raw URL and key for its
// WebSocket, so it calls loadCredentials directly.
func gatewayClient() (*client.Client, error) {
	cfg, err := loadCredentials()
	if err != nil {
		return nil, err
	}
	return client.New(cfg.GatewayURL, cfg.APIKey), nil
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", build.Name, err)
		return 1
	}
	return 0
}
