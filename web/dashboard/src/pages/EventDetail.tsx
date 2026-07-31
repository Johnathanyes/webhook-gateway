import type { ReactNode } from "react";
import { Link, useParams } from "react-router";
import { toast } from "sonner";

import { useDestinations } from "@/api/destinations";
import { useEvent, useEventTrace, type TraceAttempt, type TraceDelivery } from "@/api/events";
import { useReplayEvent } from "@/api/replays";
import { useSources } from "@/api/sources";
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatDateTime } from "@/lib/format";

// raw_body is base64 (a Go []byte). Decode through TextDecoder rather than
// atob alone so multi-byte UTF-8 payloads don't come out mangled.
function decodeBody(base64: string) {
  try {
    const bytes = Uint8Array.from(atob(base64), (c) => c.charCodeAt(0));
    return new TextDecoder().decode(bytes);
  } catch {
    return "(unreadable body)";
  }
}

function pretty(value: unknown) {
  if (value === undefined || value === null) return "—";
  return JSON.stringify(value, null, 2);
}

function statusVariant(status: string) {
  if (status === "succeeded") return "secondary" as const;
  if (status === "dead_lettered" || status === "failed") return "destructive" as const;
  return "outline" as const;
}

function Pre({ children }: { children: ReactNode }) {
  return (
    <pre className="max-h-80 overflow-auto rounded-md bg-muted p-3 text-xs">{children}</pre>
  );
}

export default function EventDetail() {
  const { id = "" } = useParams();
  const event = useEvent(id);
  const trace = useEventTrace(id);
  const { data: sources } = useSources();
  const { data: destinations } = useDestinations();

  const sourceName = (sourceId: string) => sources?.find((s) => s.id === sourceId)?.name ?? sourceId;
  const destinationName = (destId: string) =>
    destinations?.find((d) => d.id === destId)?.name ?? destId;

  if (event.isPending) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (event.error) return <p className="text-sm text-destructive">{event.error.message}</p>;

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between">
        <div className="space-y-2">
          <Button variant="ghost" size="sm">
            <Link to="/events">← Events</Link>
          </Button>
          <h1 className="text-2xl font-semibold">Event</h1>
          <code className="text-xs text-muted-foreground">{event.data.id}</code>
        </div>
        <ReplayButton eventId={event.data.id} />
      </div>

      {/* received → verified → queued → attempts → outcome, in that order. */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Received</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-4 text-sm sm:grid-cols-4">
          <div>
            <div className="text-muted-foreground">Source</div>
            <div>{sourceName(event.data.source_id)}</div>
          </div>
          <div>
            <div className="text-muted-foreground">At</div>
            <div>{formatDateTime(event.data.received_at)}</div>
          </div>
          <div>
            <div className="text-muted-foreground">Signature</div>
            <Badge variant={event.data.verified ? "secondary" : "destructive"}>
              {event.data.verified ? "verified" : "unverified"}
            </Badge>
          </div>
          <div>
            <div className="text-muted-foreground">Dedupe key</div>
            <div className="truncate">{event.data.dedupe_key ?? "—"}</div>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Payload</CardTitle>
        </CardHeader>
        <CardContent>
          <Tabs defaultValue="body">
            <TabsList>
              <TabsTrigger value="body">Body</TabsTrigger>
              <TabsTrigger value="parsed">Parsed</TabsTrigger>
              <TabsTrigger value="headers">Headers</TabsTrigger>
            </TabsList>
            <TabsContent value="body">
              <Pre>{decodeBody(event.data.raw_body)}</Pre>
            </TabsContent>
            <TabsContent value="parsed">
              <Pre>{pretty(event.data.parsed_body)}</Pre>
            </TabsContent>
            <TabsContent value="headers">
              <Pre>{pretty(event.data.raw_headers)}</Pre>
            </TabsContent>
          </Tabs>
        </CardContent>
      </Card>

      <div className="space-y-3">
        <h2 className="text-lg font-semibold">Deliveries</h2>

        {trace.isPending && <p className="text-sm text-muted-foreground">Loading trace…</p>}
        {trace.error && <p className="text-sm text-destructive">{trace.error.message}</p>}

        {trace.data?.deliveries.length === 0 && (
          <p className="text-sm text-muted-foreground">
            No deliveries. Either no route matched this event, or a rule dropped it.
          </p>
        )}

        {trace.data?.deliveries.map((delivery) => (
          <DeliveryCard
            key={delivery.delivery_id}
            delivery={delivery}
            destinationName={destinationName(delivery.destination_id)}
          />
        ))}
      </div>
    </div>
  );
}

// Replay creates fresh deliveries rather than retrying the existing ones, so
// the trace below grows a new delivery card instead of new attempts on the old.
function ReplayButton({ eventId }: { eventId: string }) {
  const replay = useReplayEvent();

  return (
    <AlertDialog>
      <AlertDialogTrigger>
        <Button disabled={replay.isPending}>{replay.isPending ? "Replaying…" : "Replay"}</Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Replay this event?</AlertDialogTitle>
          <AlertDialogDescription>
            It runs back through the normal delivery path — one new delivery per enabled route, with
            a new Webhook-Id and the destination's usual retry policy. A destination that isn't
            idempotent will process it twice.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            onClick={() =>
              replay.mutate(eventId, {
                onSuccess: (result) =>
                  toast.success(
                    result.deliveries_created === 0
                      ? "No enabled routes for this source — nothing to deliver"
                      : `${result.deliveries_created} deliver${
                          result.deliveries_created === 1 ? "y" : "ies"
                        } created`,
                  ),
              })
            }
          >
            Replay
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function DeliveryCard({
  delivery,
  destinationName,
}: {
  delivery: TraceDelivery;
  destinationName: string;
}) {
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0">
        <CardTitle className="text-base">{destinationName}</CardTitle>
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <span>queued {formatDateTime(delivery.queued_at)}</span>
          <span>·</span>
          <span>
            {delivery.attempt_count} attempt{delivery.attempt_count === 1 ? "" : "s"}
          </span>
          <Badge variant={statusVariant(delivery.status)}>{delivery.status.replace("_", " ")}</Badge>
        </div>
      </CardHeader>
      <CardContent>
        {delivery.dead_lettered_at && (
          <p className="mb-3 text-sm text-destructive">
            Dead-lettered {formatDateTime(delivery.dead_lettered_at)} — out of attempts.
          </p>
        )}

        {delivery.attempts.length === 0 ? (
          <p className="text-sm text-muted-foreground">No attempts yet.</p>
        ) : (
          <Accordion>
            {delivery.attempts.map((attempt) => (
              <AttemptRow key={attempt.attempt_number} attempt={attempt} />
            ))}
          </Accordion>
        )}
      </CardContent>
    </Card>
  );
}

function AttemptRow({ attempt }: { attempt: TraceAttempt }) {
  // An attempt either got an HTTP response or failed to get one (timeout,
  // connection refused); the trace is only useful if it shows both.
  const outcome = attempt.response_status_code
    ? `HTTP ${attempt.response_status_code}`
    : (attempt.error ?? "no response");

  return (
    <AccordionItem value={String(attempt.attempt_number)}>
      <AccordionTrigger>
        <div className="flex w-full items-center gap-3 pr-4 text-sm">
          <span className="font-medium">Attempt {attempt.attempt_number}</span>
          <span className={attempt.response_status_code ? "" : "text-destructive"}>{outcome}</span>
          <span className="ml-auto text-muted-foreground">
            {attempt.duration_ms !== undefined ? `${attempt.duration_ms}ms` : "—"} ·{" "}
            {formatDateTime(attempt.attempted_at)}
          </span>
        </div>
      </AccordionTrigger>
      <AccordionContent className="space-y-4">
        {attempt.error && (
          <div>
            <div className="mb-1 text-sm text-muted-foreground">Error</div>
            <Pre>{attempt.error}</Pre>
          </div>
        )}
        <div>
          <div className="mb-1 text-sm text-muted-foreground">Request headers</div>
          <Pre>{pretty(attempt.request_headers)}</Pre>
        </div>
        <div>
          <div className="mb-1 text-sm text-muted-foreground">Response headers</div>
          <Pre>{pretty(attempt.response_headers)}</Pre>
        </div>
        <div>
          <div className="mb-1 text-sm text-muted-foreground">Response body (truncated at storage)</div>
          <Pre>{attempt.response_body_truncated || "—"}</Pre>
        </div>
      </AccordionContent>
    </AccordionItem>
  );
}
