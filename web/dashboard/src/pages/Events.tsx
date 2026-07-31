import { useState } from "react";
import { Link, useSearchParams } from "react-router";

import { deliveryStatuses, useEvents, type EventFilters } from "@/api/events";
import { useSources } from "@/api/sources";
import BulkReplayDialog from "@/components/BulkReplayDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatDateTime } from "@/lib/format";

// shadcn's Select cannot hold an empty value, so "any" stands in for
// "filter not applied" and is stripped before it reaches the query string.
const ANY = "any";

// datetime-local gives "2026-07-31T14:03"; the API wants RFC 3339.
function toRFC3339(local: string) {
  return local ? new Date(local).toISOString() : "";
}

function toLocalInput(rfc3339: string) {
  if (!rfc3339) return "";
  const d = new Date(rfc3339);
  // Trim the timezone and seconds the input control won't accept.
  return new Date(d.getTime() - d.getTimezoneOffset() * 60_000).toISOString().slice(0, 16);
}

export default function Events() {
  // Filters live in the URL: a filtered view survives a refresh, is
  // shareable, and comes back intact when you navigate back from a trace.
  const [searchParams, setSearchParams] = useSearchParams();
  const [searchDraft, setSearchDraft] = useState(searchParams.get("search") ?? "");

  const filters: EventFilters = Object.fromEntries(searchParams.entries());
  const { data, isPending, error, fetchNextPage, hasNextPage, isFetchingNextPage } = useEvents(filters);
  const { data: sources } = useSources();

  function setFilter(key: keyof EventFilters, value: string) {
    const next = new URLSearchParams(searchParams);
    if (value && value !== ANY) {
      next.set(key, value);
    } else {
      next.delete(key);
    }
    setSearchParams(next, { replace: true });
  }

  const sourceName = (id: string) => sources?.find((s) => s.id === id)?.name ?? id;
  const events = data?.pages.flatMap((page) => page.events) ?? [];

  return (
    <div className="space-y-6">
      <header className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Events</h1>
          <p className="text-sm text-muted-foreground">
            Everything that arrived, verified or not. Open one to see its full delivery trace.
          </p>
        </div>
        <BulkReplayDialog filters={filters} sourceName={sourceName} />
      </header>

      <div className="grid gap-4 md:grid-cols-3 lg:grid-cols-5">
        <form
          className="space-y-1.5"
          onSubmit={(e) => {
            e.preventDefault();
            setFilter("search", searchDraft);
          }}
        >
          <Label htmlFor="search">Search</Label>
          <Input
            id="search"
            value={searchDraft}
            onChange={(e) => setSearchDraft(e.target.value)}
            placeholder="payload or header text"
          />
        </form>

        <div className="space-y-1.5">
          <Label htmlFor="filter-source">Source</Label>
          <Select value={filters.source_id ?? ANY} onValueChange={(v) => v && setFilter("source_id", v)}>
            <SelectTrigger id="filter-source">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ANY}>Any source</SelectItem>
              {sources?.map((source) => (
                <SelectItem key={source.id} value={source.id}>
                  {source.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="filter-verified">Verified</Label>
          <Select value={filters.verified ?? ANY} onValueChange={(v) => v && setFilter("verified", v)}>
            <SelectTrigger id="filter-verified">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ANY}>Any</SelectItem>
              <SelectItem value="true">Verified</SelectItem>
              <SelectItem value="false">Unverified</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="filter-status">Delivery status</Label>
          <Select
            value={filters.delivery_status ?? ANY}
            onValueChange={(v) => v && setFilter("delivery_status", v)}
          >
            <SelectTrigger id="filter-status">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ANY}>Any status</SelectItem>
              {deliveryStatuses.map((status) => (
                <SelectItem key={status} value={status}>
                  {status.replace("_", " ")}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1.5">
            <Label htmlFor="filter-after">After</Label>
            <Input
              id="filter-after"
              type="datetime-local"
              value={toLocalInput(filters.after ?? "")}
              onChange={(e) => setFilter("after", toRFC3339(e.target.value))}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="filter-before">Before</Label>
            <Input
              id="filter-before"
              type="datetime-local"
              value={toLocalInput(filters.before ?? "")}
              onChange={(e) => setFilter("before", toRFC3339(e.target.value))}
            />
          </div>
        </div>
      </div>

      {searchParams.size > 0 && (
        <Button
          variant="ghost"
          size="sm"
          onClick={() => {
            setSearchDraft("");
            setSearchParams(new URLSearchParams(), { replace: true });
          }}
        >
          Clear filters
        </Button>
      )}

      {isPending && <p className="text-sm text-muted-foreground">Loading…</p>}
      {error && <p className="text-sm text-destructive">{error.message}</p>}

      {data && events.length === 0 && (
        <p className="text-sm text-muted-foreground">No events match these filters.</p>
      )}

      {events.length > 0 && (
        <>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Received</TableHead>
                <TableHead>Source</TableHead>
                <TableHead>Verified</TableHead>
                <TableHead>Content type</TableHead>
                <TableHead>Dedupe key</TableHead>
                <TableHead className="text-right">Trace</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {events.map((event) => (
                <TableRow key={event.id}>
                  <TableCell className="whitespace-nowrap">{formatDateTime(event.received_at)}</TableCell>
                  <TableCell>{sourceName(event.source_id)}</TableCell>
                  <TableCell>
                    <Badge variant={event.verified ? "secondary" : "outline"}>
                      {event.verified ? "verified" : "unverified"}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-sm text-muted-foreground">
                    {event.content_type ?? "—"}
                  </TableCell>
                  <TableCell className="max-w-40 truncate text-sm text-muted-foreground">
                    {event.dedupe_key ?? "—"}
                  </TableCell>
                  <TableCell className="text-right">
                    <Button
                      variant="outline"
                      size="sm"
                      render={<Link to={`/events/${event.id}`} />}
                    >
                      View
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>

          {hasNextPage && (
            <Button variant="outline" onClick={() => fetchNextPage()} disabled={isFetchingNextPage}>
              {isFetchingNextPage ? "Loading…" : "Load more"}
            </Button>
          )}
        </>
      )}
    </div>
  );
}
