package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"webhook-gateway-cli/internal/client"
	"webhook-gateway-cli/internal/forward"
)

// replayTimeout bounds one local POST. Replay is interactive — a developer is
// watching — so it gives up well before a webhook's production retry budget.
const replayTimeout = 30 * time.Second

func newReplayCmd() *cobra.Command {
	var forwardTo, source string
	var last int

	cmd := &cobra.Command{
		Use:   "replay [event-id]",
		Short: "Re-send a stored event to a local URL",
		Long: "Fetches an event the gateway already received and POSTs it to a local\n" +
			"server with its original headers and byte-exact body — the same request\n" +
			"the provider made, without asking the provider to make it again.\n\n" +
			"Give an event id, or use --last with --source to replay the most recent\n" +
			"events for a source. Nothing is re-delivered to real destinations: this\n" +
			"is a purely local replay.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var eventID string
			if len(args) == 1 {
				eventID = args[0]
			}
			return runReplay(cmd, eventID, source, forwardTo, last)
		},
	}
	cmd.Flags().StringVar(&forwardTo, "forward-to", "", "local URL to replay to, e.g. localhost:3000")
	cmd.Flags().StringVar(&source, "source", "", "source name, used with --last")
	cmd.Flags().IntVar(&last, "last", 0, "replay the N most recent events instead of one id")
	_ = cmd.MarkFlagRequired("forward-to")
	return cmd
}

func runReplay(cmd *cobra.Command, eventID, source, forwardTo string, last int) error {
	switch {
	case eventID == "" && last <= 0:
		return errors.New("give an event id, or use --last N")
	case eventID != "" && last > 0:
		return errors.New("give an event id or --last N, not both")
	case last < 0:
		return errors.New("--last must be positive")
	}

	api, err := gatewayClient()
	if err != nil {
		return err
	}
	target, err := client.NormalizeURL(forwardTo)
	if err != nil {
		return fmt.Errorf("--forward-to: %w", err)
	}

	ctx := cmd.Context()
	ids, err := replayTargets(ctx, api, eventID, source, last)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No matching events to replay.")
		return nil
	}

	// One name lookup up front labels every line without a request per event.
	names, err := sourceNames(ctx, api)
	if err != nil {
		return err
	}

	httpClient := &http.Client{Timeout: replayTimeout}
	var failures int
	for _, id := range ids {
		if err := replayOne(ctx, api, httpClient, cmd.OutOrStdout(), names, id, target); err != nil {
			failures++
			fmt.Fprintf(cmd.OutOrStdout(), "%s\n", err)
		}
	}
	if failures > 0 {
		return fmt.Errorf("%d of %d events failed to replay", failures, len(ids))
	}
	return nil
}

// replayOne fetches and re-sends a single event, printing the same line format
// `listen` uses so the two commands read identically.
func replayOne(ctx context.Context, api *client.Client, httpClient *http.Client, out io.Writer, names map[string]string, id, target string) error {
	event, err := api.GetEvent(ctx, id)
	if err != nil {
		return fmt.Errorf("fetching event %s: %w", id, err)
	}

	label := names[event.SourceID]
	if label == "" {
		label = "event " + shortID(event.ID)
	}

	res, postErr := forward.Post(ctx, httpClient, forward.Request{
		Target:  target,
		Headers: event.RawHeaders,
		Body:    event.RawBody,
		// No Webhook-Id: this is a local replay, not a gateway delivery, so
		// there is no delivery id to name.
	})
	fmt.Fprintf(out, "%s\n", forward.Line(label, forward.EventType(event.RawHeaders, event.RawBody), res, postErr))
	if postErr != nil {
		return fmt.Errorf("replaying %s: %w", shortID(id), postErr)
	}
	return nil
}

// replayTargets resolves the command's arguments to the event ids to replay,
// newest first.
func replayTargets(ctx context.Context, api *client.Client, eventID, source string, last int) ([]string, error) {
	if eventID != "" {
		return []string{eventID}, nil
	}

	var sourceID string
	if source != "" {
		found, err := api.SourceByName(ctx, source)
		if err != nil {
			return nil, err
		}
		sourceID = found.ID
	}

	events, err := api.ListEvents(ctx, sourceID, last)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(events))
	for i, e := range events {
		ids[i] = e.ID
	}
	return ids, nil
}

// sourceNames maps source id to name for display.
func sourceNames(ctx context.Context, api *client.Client) (map[string]string, error) {
	sources, err := api.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(sources))
	for _, s := range sources {
		names[s.ID] = s.Name
	}
	return names, nil
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
