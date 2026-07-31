import { useState } from "react";

import { useDestinations } from "@/api/destinations";
import { useCreateRoute, useDeleteRoute, useRoutes, useSetRouteEnabled } from "@/api/routes";
import { useSources } from "@/api/sources";
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
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

export default function RoutesPage() {
  const { data: routes, isPending, error } = useRoutes();
  const { data: sources } = useSources();
  const { data: destinations } = useDestinations();
  const setEnabled = useSetRouteEnabled();
  const deleteRoute = useDeleteRoute();

  // Routes carry only IDs, so the names come from the other two lists.
  const sourceName = (id: string) => sources?.find((s) => s.id === id)?.name ?? id;
  const destinationName = (id: string) => destinations?.find((d) => d.id === id)?.name ?? id;

  return (
    <div className="space-y-6">
      <header className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Routes</h1>
          <p className="text-sm text-muted-foreground">
            Bind a source to a destination. An event with no matching route is stored and delivered
            nowhere.
          </p>
        </div>
        <NewRouteDialog />
      </header>

      {isPending && <p className="text-sm text-muted-foreground">Loading…</p>}
      {error && <p className="text-sm text-destructive">{error.message}</p>}

      {routes && routes.length === 0 && <p className="text-sm text-muted-foreground">No routes yet.</p>}

      {routes && routes.length > 0 && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Source</TableHead>
              <TableHead>Destination</TableHead>
              <TableHead>Enabled</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {routes.map((route) => (
              <TableRow key={route.id}>
                <TableCell className="font-medium">{sourceName(route.source_id)}</TableCell>
                <TableCell>{destinationName(route.destination_id)}</TableCell>
                <TableCell>
                  <Switch
                    checked={route.enabled}
                    disabled={setEnabled.isPending}
                    onCheckedChange={(checked) =>
                      setEnabled.mutate({ id: route.id, enabled: checked })
                    }
                  />
                </TableCell>
                <TableCell className="text-right">
                  <Button variant="outline" size="sm" onClick={() => deleteRoute.mutate(route.id)}>
                    Delete
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}

function NewRouteDialog() {
  const [open, setOpen] = useState(false);
  const [sourceId, setSourceId] = useState("");
  const [destinationId, setDestinationId] = useState("");

  const { data: sources } = useSources();
  const { data: destinations } = useDestinations();
  const createRoute = useCreateRoute();

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) {
          setSourceId("");
          setDestinationId("");
          createRoute.reset();
        }
      }}
    >
      <DialogTrigger>
        <Button>New route</Button>
      </DialogTrigger>

      <DialogContent>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            createRoute.mutate(
              { source_id: sourceId, destination_id: destinationId },
              { onSuccess: () => setOpen(false) },
            );
          }}
        >
          <DialogHeader>
            <DialogTitle>New route</DialogTitle>
            <DialogDescription>Every event from the source fans out to this destination.</DialogDescription>
          </DialogHeader>

          <div className="space-y-4 py-4">
            <div className="space-y-1.5">
              <Label htmlFor="route-source">Source</Label>
              <Select value={sourceId} onValueChange={(value) => value && setSourceId(value)}>
                <SelectTrigger id="route-source">
                  <SelectValue placeholder="Select a source" />
                </SelectTrigger>
                <SelectContent>
                  {sources?.map((source) => (
                    <SelectItem key={source.id} value={source.id}>
                      {source.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="route-destination">Destination</Label>
              <Select value={destinationId} onValueChange={(value) => value && setDestinationId(value)}>
                <SelectTrigger id="route-destination">
                  <SelectValue placeholder="Select a destination" />
                </SelectTrigger>
                <SelectContent>
                  {destinations?.map((destination) => (
                    <SelectItem key={destination.id} value={destination.id}>
                      {destination.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            {createRoute.isError && (
              <p role="alert" className="text-sm text-destructive">
                {createRoute.error.message}
              </p>
            )}
          </div>

          <DialogFooter>
            <Button type="submit" disabled={!sourceId || !destinationId || createRoute.isPending}>
              {createRoute.isPending ? "Creating…" : "Create route"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
