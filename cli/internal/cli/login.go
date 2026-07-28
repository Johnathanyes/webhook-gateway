package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"webhook-gateway-cli/internal/build"
	"webhook-gateway-cli/internal/client"
	"webhook-gateway-cli/internal/config"
)

const defaultGatewayURL = "http://localhost:8080"

func newLoginCmd() *cobra.Command {
	var gatewayURL, apiKey string

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Store credentials for a gateway",
		Long: fmt.Sprintf(
			"Prompts for a gateway URL and API key, verifies them against the gateway,\n"+
				"and stores them for later commands.\n\n"+
				"Both values can be supplied as flags instead, for scripted setup. The\n"+
				"credential is an API key (%s...) with at least the `read` scope, or the\n"+
				"gateway's admin password.", "whg_"),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLogin(cmd, gatewayURL, apiKey)
		},
	}
	cmd.Flags().StringVar(&gatewayURL, "url", "", "gateway base URL (prompted if omitted)")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "API key or admin password (prompted if omitted)")
	return cmd
}

func runLogin(cmd *cobra.Command, gatewayURL, apiKey string) error {
	out := cmd.OutOrStdout()
	in := cmd.InOrStdin()
	reader := bufio.NewReader(in)

	if gatewayURL == "" {
		var err error
		if gatewayURL, err = promptURL(out, reader); err != nil {
			return err
		}
	}
	normalized, err := client.NormalizeURL(gatewayURL)
	if err != nil {
		return err
	}

	if apiKey == "" {
		if apiKey, err = promptAPIKey(out, in, reader); err != nil {
			return err
		}
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return errors.New("API key is required")
	}

	// Verify before storing: writing credentials that don't work only moves the
	// failure to the next command, where it is harder to diagnose.
	sources, err := client.New(normalized, apiKey).ListSources(context.Background())
	if err != nil {
		return loginError(normalized, err)
	}

	path, err := config.Save(config.Config{GatewayURL: normalized, APIKey: apiKey})
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "Logged in to %s\n", normalized)
	_, _ = fmt.Fprintf(out, "Credentials saved to %s\n", path)
	_, _ = fmt.Fprintf(out, "%s configured.\n", pluralize(len(sources), "source", "sources"))
	if len(sources) == 0 {
		_, _ = fmt.Fprintf(out, "\nNo sources yet — create one to start receiving webhooks.\n")
	}
	return nil
}

// promptURL asks for the gateway, defaulting to a previous login's gateway or
// the local dev default.
func promptURL(out io.Writer, reader *bufio.Reader) (string, error) {
	fallback := defaultGatewayURL
	if existing, err := config.Load(); err == nil && existing.GatewayURL != "" {
		fallback = existing.GatewayURL
	}

	_, _ = fmt.Fprintf(out, "Gateway URL [%s]: ", fallback)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("reading gateway URL: %w", err)
	}
	if line = strings.TrimSpace(line); line != "" {
		return line, nil
	}
	return fallback, nil
}

// promptAPIKey reads the credential without echoing it when the command is
// attached to a real terminal, so the key never lands in scrollback or a screen
// recording. Piped input reads normally — there is nothing to echo to.
func promptAPIKey(out io.Writer, in io.Reader, reader *bufio.Reader) (string, error) {
	_, _ = fmt.Fprint(out, "API key: ")

	if in == os.Stdin && term.IsTerminal(int(os.Stdin.Fd())) {
		secret, err := term.ReadPassword(int(os.Stdin.Fd()))
		_, _ = fmt.Fprintln(out)
		if err != nil {
			return "", fmt.Errorf("reading API key: %w", err)
		}
		return string(secret), nil
	}

	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("reading API key: %w", err)
	}
	return line, nil
}

// loginError turns the gateway's response into advice, since the two failures a
// developer actually hits here are a wrong credential and a missing scope.
func loginError(gatewayURL string, err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusUnauthorized:
			return fmt.Errorf("%s rejected the credential — the key may be wrong or revoked", gatewayURL)
		case http.StatusForbidden:
			return fmt.Errorf("that key lacks the `read` scope, which %s login requires", build.Name)
		}
		return err
	}
	return fmt.Errorf("could not reach a gateway at %s: %w", gatewayURL, err)
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}
