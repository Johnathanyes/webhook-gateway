package cli

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"webhook-gateway-cli/internal/build"
	"webhook-gateway-cli/internal/client"
	"webhook-gateway-cli/internal/config"
	"webhook-gateway-cli/internal/tunnel"
)

func newListenCmd() *cobra.Command {
	var source, forwardTo string

	cmd := &cobra.Command{
		Use:   "listen",
		Short: "Forward gateway events to a local URL",
		Long: fmt.Sprintf(
			"Opens a tunnel to the gateway and forwards matching events to a local\n"+
				"server, replaying each provider's original headers and body so local\n"+
				"signature verification works exactly as it does in production.\n\n"+
				"The credential stored by `%s login` needs the `tunnel` scope.\n"+
				"Reconnects automatically; press Ctrl-C to stop.", build.Name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runListen(cmd, source, forwardTo)
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "source name to listen for (default: all sources)")
	cmd.Flags().StringVar(&forwardTo, "forward-to", "", "local URL to forward events to, e.g. localhost:3000")
	_ = cmd.MarkFlagRequired("forward-to")
	return cmd
}

func runListen(cmd *cobra.Command, source, forwardTo string) error {
	cfg, err := config.Load()
	if err != nil {
		if errors.Is(err, config.ErrNotLoggedIn) {
			return fmt.Errorf("not logged in — run `%s login` first", build.Name)
		}
		return err
	}

	// The same normalization the login prompt uses, so `localhost:3000` works
	// as a forwarding target just like it does as a gateway URL.
	target, err := client.NormalizeURL(forwardTo)
	if err != nil {
		return fmt.Errorf("--forward-to: %w", err)
	}

	// Ctrl-C cancels the session; Listen drains in-flight events and returns.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return tunnel.Listen(ctx, tunnel.Options{
		GatewayURL: cfg.GatewayURL,
		APIKey:     cfg.APIKey,
		Source:     source,
		ForwardTo:  target,
		Out:        cmd.OutOrStdout(),
	})
}
