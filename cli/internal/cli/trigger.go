package cli

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"webhook-gateway-cli/internal/client"
)

func newTriggerCmd() *cobra.Command {
	var source string

	cmd := &cobra.Command{
		Use:   "trigger",
		Short: "Send a sample event through a source",
		Long: "Asks the gateway to sign its built-in sample payload with the source's\n" +
			"real signing secret and run it through the normal ingest path — so the\n" +
			"event is verified, stored, and delivered exactly like a real one.\n\n" +
			"With `listen` running, this exercises the whole local loop without\n" +
			"configuring anything at the provider. Needs the `write` scope.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTrigger(cmd, source)
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "source name to send the sample event through")
	_ = cmd.MarkFlagRequired("source")
	return cmd
}

func runTrigger(cmd *cobra.Command, source string) error {
	api, err := gatewayClient()
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	found, err := api.SourceByName(ctx, source)
	if err != nil {
		return err
	}

	result, err := api.TriggerTestEvent(ctx, found.ID)
	if err != nil {
		return triggerError(found, err)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Triggered a %s sample event through %s\n", found.ProviderType, found.Name)
	fmt.Fprintf(out, "Event %s stored (verified: %t)\n", result.EventID, result.Verified)
	if !result.Verified {
		// The gateway signs with the source's stored secret, so a failure here
		// means that secret can't produce a signature its own verifier accepts.
		fmt.Fprintf(out, "\nThe sample failed verification — check this source's signing secret.\n")
	}
	return nil
}

// triggerError explains the two failures specific to this command: a key that
// can't write, and a provider with no sample payload in the catalog.
func triggerError(source client.Source, err error) error {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.Status {
	case http.StatusForbidden:
		return errors.New("that key lacks the `write` scope, which trigger requires")
	case http.StatusBadRequest:
		return fmt.Errorf("provider %q has no sample payload to send: %s", source.ProviderType, apiErr.Message)
	}
	return err
}
