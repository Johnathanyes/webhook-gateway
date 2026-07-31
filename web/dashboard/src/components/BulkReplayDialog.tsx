import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";

import type { EventFilters } from "@/api/events";
import { toReplayFilter, useBulkReplay, useReplay } from "@/api/replays";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";

// Describes the match set in the same words the filter bar uses, so the
// confirmation says exactly what is about to be re-delivered.
function describe(filters: EventFilters, sourceName: (id: string) => string) {
  const parts: string[] = [];
  if (filters.source_id) parts.push(`source ${sourceName(filters.source_id)}`);
  if (filters.verified) parts.push(filters.verified === "true" ? "verified only" : "unverified only");
  if (filters.delivery_status) parts.push(`delivery ${filters.delivery_status.replace("_", " ")}`);
  if (filters.search) parts.push(`matching "${filters.search}"`);
  if (filters.after) parts.push(`after ${new Date(filters.after).toLocaleString()}`);
  if (filters.before) parts.push(`before ${new Date(filters.before).toLocaleString()}`);
  return parts;
}

export default function BulkReplayDialog({
  filters,
  sourceName,
}: {
  filters: EventFilters;
  sourceName: (id: string) => string;
}) {
  const [open, setOpen] = useState(false);
  const [replayId, setReplayId] = useState<string | null>(null);

  const queryClient = useQueryClient();
  const bulkReplay = useBulkReplay();
  const replay = useReplay(replayId);

  const criteria = describe(filters, sourceName);
  const running = replay.data?.status === "running";

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) {
          setReplayId(null);
          bulkReplay.reset();
          // Delivery-status filters see different results once a replay has
          // requeued things.
          queryClient.invalidateQueries({ queryKey: ["events"] });
        }
      }}
    >
      <DialogTrigger>
        <Button variant="outline">Replay these results</Button>
      </DialogTrigger>

      <DialogContent>
        <DialogHeader>
          <DialogTitle>Replay matching events</DialogTitle>
          <DialogDescription>
            Each matched event is re-run through the normal delivery path — new deliveries, new
            Webhook-Id, the usual retry policy. Nothing is deleted or overwritten.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2 text-sm">
          {criteria.length > 0 ? (
            <div>
              <div className="text-muted-foreground">Filters</div>
              <ul className="list-inside list-disc">
                {criteria.map((part) => (
                  <li key={part}>{part}</li>
                ))}
              </ul>
            </div>
          ) : (
            <p className="text-destructive">
              No filters are set. This replays <strong>every stored event</strong>. Narrow the list
              first unless that is really what you want.
            </p>
          )}

          {replay.data && (
            <div className="rounded-md bg-muted p-3">
              <div className="font-medium">
                {replay.data.status === "running" && "Replaying…"}
                {replay.data.status === "completed" && "Replay complete"}
                {replay.data.status === "failed" && "Replay failed"}
              </div>
              <div className="text-muted-foreground">
                {replay.data.matched_count ?? 0} matched · {replay.data.requeued_count ?? 0} requeued
              </div>
            </div>
          )}
        </div>

        <DialogFooter>
          {replay.data && !running ? (
            <Button onClick={() => setOpen(false)}>Done</Button>
          ) : (
            <Button
              disabled={bulkReplay.isPending || running}
              onClick={() =>
                bulkReplay.mutate(toReplayFilter(filters), {
                  onSuccess: (created) => setReplayId(created.id),
                })
              }
            >
              {bulkReplay.isPending || running ? "Replaying…" : "Replay"}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
