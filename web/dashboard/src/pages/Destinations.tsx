import { useState, type ReactNode } from "react";

import {
  useCreateDestination,
  useDeleteDestination,
  useDestinations,
  useSetDestinationPaused,
  useUpdateDestination,
  type Destination,
  type DestinationInput,
} from "@/api/destinations";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
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
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

// Retry defaults that match the delivery worker's own: five attempts with
// exponential backoff from 2s, capped at five minutes.
const defaults: DestinationInput = {
  name: "",
  url: "",
  timeout_ms: 5000,
  max_attempts: 5,
  backoff_base_seconds: 2,
  backoff_max_seconds: 300,
  rate_limit_per_second: null,
};

export default function Destinations() {
  const { data: destinations, isPending, error } = useDestinations();
  const setPaused = useSetDestinationPaused();
  const deleteDestination = useDeleteDestination();

  return (
    <div className="space-y-6">
      <header className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Destinations</h1>
          <p className="text-sm text-muted-foreground">
            Where events are delivered, and how hard the gateway retries.
          </p>
        </div>
        <DestinationDialog trigger={<Button>New destination</Button>} />
      </header>

      {isPending && <p className="text-sm text-muted-foreground">Loading…</p>}
      {error && <p className="text-sm text-destructive">{error.message}</p>}

      {destinations && destinations.length === 0 && (
        <p className="text-sm text-muted-foreground">No destinations yet.</p>
      )}

      {destinations && destinations.length > 0 && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>URL</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Retries</TableHead>
              <TableHead>Rate limit</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {destinations.map((destination) => (
              <TableRow key={destination.id}>
                <TableCell className="font-medium">{destination.name}</TableCell>
                <TableCell>
                  <code className="text-xs">{destination.url}</code>
                </TableCell>
                <TableCell>
                  <Badge variant={destination.paused ? "outline" : "secondary"}>
                    {destination.paused ? "paused" : "active"}
                  </Badge>
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {destination.max_attempts}× · {destination.timeout_ms}ms timeout
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {destination.rate_limit_per_second === null
                    ? "unlimited"
                    : `${destination.rate_limit_per_second}/s`}
                </TableCell>
                <TableCell className="space-x-2 text-right">
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={setPaused.isPending}
                    onClick={() =>
                      setPaused.mutate({ id: destination.id, paused: !destination.paused })
                    }
                  >
                    {destination.paused ? "Resume" : "Pause"}
                  </Button>

                  <DestinationDialog
                    destination={destination}
                    trigger={
                      <Button variant="outline" size="sm">
                        Edit
                      </Button>
                    }
                  />

                  <AlertDialog>
                    <AlertDialogTrigger>
                      <Button variant="outline" size="sm">
                        Delete
                      </Button>
                    </AlertDialogTrigger>
                    <AlertDialogContent>
                      <AlertDialogHeader>
                        <AlertDialogTitle>Delete {destination.name}?</AlertDialogTitle>
                        <AlertDialogDescription>
                          Routes pointing at this destination go with it. Events already delivered are
                          kept.
                        </AlertDialogDescription>
                      </AlertDialogHeader>
                      <AlertDialogFooter>
                        <AlertDialogCancel>Cancel</AlertDialogCancel>
                        <AlertDialogAction onClick={() => deleteDestination.mutate(destination.id)}>
                          Delete
                        </AlertDialogAction>
                      </AlertDialogFooter>
                    </AlertDialogContent>
                  </AlertDialog>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}

// One form for both create and edit — the fields are identical, and PATCH takes
// the same body as POST.
function DestinationDialog({
  destination,
  trigger,
}: {
  destination?: Destination;
  trigger: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState<DestinationInput>(destination ?? defaults);

  const create = useCreateDestination();
  const update = useUpdateDestination();
  const mutation = destination ? update : create;

  function submit() {
    const onSuccess = () => setOpen(false);
    if (destination) {
      update.mutate({ id: destination.id, input: form }, { onSuccess });
    } else {
      create.mutate(form, { onSuccess });
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        // Reopening should start from the saved values, not half-finished edits.
        if (!next) {
          setForm(destination ?? defaults);
          mutation.reset();
        }
      }}
    >
      <DialogTrigger>{trigger}</DialogTrigger>

      <DialogContent className="sm:max-w-lg">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          <DialogHeader>
            <DialogTitle>{destination ? "Edit destination" : "New destination"}</DialogTitle>
            <DialogDescription>
              Delivery is retried with exponential backoff until it succeeds or runs out of attempts.
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 py-4">
            <div className="space-y-1.5">
              <Label htmlFor="dest-name">Name</Label>
              <Input
                id="dest-name"
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
                placeholder="orders-service"
                autoFocus
              />
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="dest-url">URL</Label>
              <Input
                id="dest-url"
                type="url"
                value={form.url}
                onChange={(e) => setForm({ ...form, url: e.target.value })}
                placeholder="https://api.example.com/webhooks"
              />
            </div>

            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-1.5">
                <Label htmlFor="timeout">Timeout (ms)</Label>
                <Input
                  id="timeout"
                  type="number"
                  min={1}
                  value={form.timeout_ms}
                  onChange={(e) => setForm({ ...form, timeout_ms: Number(e.target.value) })}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="attempts">Max attempts</Label>
                <Input
                  id="attempts"
                  type="number"
                  min={1}
                  value={form.max_attempts}
                  onChange={(e) => setForm({ ...form, max_attempts: Number(e.target.value) })}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="backoff-base">Backoff base (s)</Label>
                <Input
                  id="backoff-base"
                  type="number"
                  min={1}
                  value={form.backoff_base_seconds}
                  onChange={(e) =>
                    setForm({ ...form, backoff_base_seconds: Number(e.target.value) })
                  }
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="backoff-max">Backoff max (s)</Label>
                <Input
                  id="backoff-max"
                  type="number"
                  min={1}
                  value={form.backoff_max_seconds}
                  onChange={(e) => setForm({ ...form, backoff_max_seconds: Number(e.target.value) })}
                />
              </div>
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="rate">Rate limit (per second)</Label>
              <Input
                id="rate"
                type="number"
                min={1}
                value={form.rate_limit_per_second ?? ""}
                placeholder="unlimited"
                onChange={(e) =>
                  setForm({
                    ...form,
                    rate_limit_per_second: e.target.value === "" ? null : Number(e.target.value),
                  })
                }
              />
              <p className="text-xs text-muted-foreground">Leave empty for unlimited.</p>
            </div>

            {mutation.isError && (
              <p role="alert" className="text-sm text-destructive">
                {mutation.error.message}
              </p>
            )}
          </div>

          <DialogFooter>
            <Button type="submit" disabled={form.name === "" || form.url === "" || mutation.isPending}>
              {mutation.isPending ? "Saving…" : destination ? "Save changes" : "Create destination"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
