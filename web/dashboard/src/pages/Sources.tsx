import { useState } from "react";
import { toast } from "sonner";

import { useProviders } from "@/api/providers";
import { useCreateSource, useSendTestEvent, useSources, type Source } from "@/api/sources";
import CopyButton from "@/components/CopyButton";
import { Badge } from "@/components/ui/badge";
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
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

// The gateway serves the SPA from its own origin, so the ingest URL a provider
// posts to is this origin plus the source's endpoint path.
function ingestUrl(source: Source) {
  return `${window.location.origin}/ingest/${source.endpoint_path}`;
}

function randomSecret() {
  const bytes = crypto.getRandomValues(new Uint8Array(32));
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

export default function Sources() {
  const { data: sources, isPending, error } = useSources();
  const testEvent = useSendTestEvent();

  return (
    <div className="space-y-6">
      <header className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Sources</h1>
          <p className="text-sm text-muted-foreground">
            Where webhooks arrive. Each source gets its own unguessable ingest URL.
          </p>
        </div>
        <NewSourceDialog />
      </header>

      {isPending && <p className="text-sm text-muted-foreground">Loading…</p>}
      {error && <p className="text-sm text-destructive">{error.message}</p>}

      {sources && sources.length === 0 && (
        <p className="text-sm text-muted-foreground">No sources yet. Create one to get an ingest URL.</p>
      )}

      {sources && sources.length > 0 && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Provider</TableHead>
              <TableHead>Ingest URL</TableHead>
              <TableHead>Dedupe</TableHead>
              <TableHead className="text-right">Test</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {sources.map((source) => (
              <TableRow key={source.id}>
                <TableCell className="font-medium">{source.name}</TableCell>
                <TableCell>
                  <Badge variant="secondary">{source.provider_type}</Badge>
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-2">
                    <code className="truncate text-xs">{ingestUrl(source)}</code>
                    <CopyButton value={ingestUrl(source)} />
                  </div>
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {source.dedupe_enabled
                    ? `${source.dedupe_strategy}${
                        source.dedupe_strategy === "field" ? ` ${source.dedupe_field_path}` : ""
                      } · ${source.dedupe_window_seconds}s`
                    : "off"}
                </TableCell>
                <TableCell className="text-right">
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={testEvent.isPending}
                    onClick={() =>
                      testEvent.mutate(source.id, {
                        onSuccess: (result) =>
                          toast.success(
                            result.verified
                              ? "Test event ingested and verified"
                              : "Test event ingested, but signature verification failed",
                          ),
                      })
                    }
                  >
                    Send test event
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

type FormState = {
  name: string;
  provider_type: string;
  signing_secret: string;
  dedupe_enabled: boolean;
  dedupe_strategy: "exact" | "field";
  dedupe_field_path: string;
  dedupe_window_seconds: string;
};

const emptyForm: FormState = {
  name: "",
  provider_type: "",
  signing_secret: "",
  dedupe_enabled: false,
  dedupe_strategy: "exact",
  dedupe_field_path: "",
  dedupe_window_seconds: "300",
};

function NewSourceDialog() {
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState(emptyForm);
  // Set once the source exists: the endpoint URL and the secret are shown here
  // and, for the secret, nowhere ever again — the gateway only keeps an
  // encrypted copy.
  const [created, setCreated] = useState<{ source: Source; secret: string } | null>(null);

  const { data: providers } = useProviders();
  const createSource = useCreateSource();

  const provider = providers?.find((p) => p.slug === form.provider_type);
  const canSubmit =
    form.name !== "" &&
    form.provider_type !== "" &&
    (!provider?.requires_secret || form.signing_secret !== "");

  function reset() {
    setForm(emptyForm);
    setCreated(null);
    createSource.reset();
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <DialogTrigger>
        <Button>New source</Button>
      </DialogTrigger>

      <DialogContent className="sm:max-w-lg">
        {created ? (
          <>
            <DialogHeader>
              <DialogTitle>Source created</DialogTitle>
              <DialogDescription>Point {created.source.provider_type} at this URL.</DialogDescription>
            </DialogHeader>

            <div className="space-y-4">
              <div className="space-y-1.5">
                <Label>Ingest URL</Label>
                <div className="flex items-center gap-2">
                  <code className="min-w-0 flex-1 truncate rounded-md bg-muted px-2 py-1.5 text-xs">
                    {ingestUrl(created.source)}
                  </code>
                  <CopyButton value={ingestUrl(created.source)} />
                </div>
              </div>

              {created.secret && (
                <div className="space-y-1.5">
                  <Label>Signing secret</Label>
                  <div className="flex items-center gap-2">
                    <code className="min-w-0 flex-1 truncate rounded-md bg-muted px-2 py-1.5 text-xs">
                      {created.secret}
                    </code>
                    <CopyButton value={created.secret} />
                  </div>
                  <p className="text-xs text-muted-foreground">
                    Shown once. The gateway stores only an encrypted copy and never returns it again.
                  </p>
                </div>
              )}
            </div>

            <DialogFooter>
              <Button onClick={() => setOpen(false)}>Done</Button>
            </DialogFooter>
          </>
        ) : (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              createSource.mutate(
                {
                  name: form.name,
                  provider_type: form.provider_type,
                  ...(form.signing_secret ? { signing_secret: form.signing_secret } : {}),
                  dedupe_enabled: form.dedupe_enabled,
                  ...(form.dedupe_enabled
                    ? {
                        dedupe_strategy: form.dedupe_strategy,
                        dedupe_field_path: form.dedupe_field_path,
                        dedupe_window_seconds: Number(form.dedupe_window_seconds) || 300,
                      }
                    : {}),
                },
                { onSuccess: (source) => setCreated({ source, secret: form.signing_secret }) },
              );
            }}
          >
            <DialogHeader>
              <DialogTitle>New source</DialogTitle>
              <DialogDescription>
                The provider decides how incoming webhooks are verified.
              </DialogDescription>
            </DialogHeader>

            <div className="space-y-4 py-4">
              <div className="space-y-1.5">
                <Label htmlFor="name">Name</Label>
                <Input
                  id="name"
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder="stripe-prod"
                  autoFocus
                />
              </div>

              <div className="space-y-1.5">
                <Label htmlFor="provider">Provider</Label>
                <Select
                  value={form.provider_type}
                  onValueChange={(value) => value && setForm({ ...form, provider_type: value })}
                >
                  <SelectTrigger id="provider">
                    <SelectValue placeholder="Select a provider" />
                  </SelectTrigger>
                  <SelectContent>
                    {providers?.map((p) => (
                      <SelectItem key={p.slug} value={p.slug}>
                        {p.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {provider?.description && (
                  <p className="text-xs text-muted-foreground">{provider.description}</p>
                )}
              </div>

              {provider?.requires_secret && (
                <div className="space-y-1.5">
                  <Label htmlFor="secret">Signing secret</Label>
                  <div className="flex gap-2">
                    <Input
                      id="secret"
                      value={form.signing_secret}
                      onChange={(e) => setForm({ ...form, signing_secret: e.target.value })}
                      placeholder="whsec_…"
                    />
                    <Button
                      type="button"
                      variant="outline"
                      onClick={() => setForm({ ...form, signing_secret: randomSecret() })}
                    >
                      Generate
                    </Button>
                  </div>
                  <p className="text-xs text-muted-foreground">
                    Paste the secret from {provider.name}, or generate one if you control the sender.
                  </p>
                </div>
              )}

              <div className="flex items-center justify-between rounded-md border p-3">
                <div>
                  <Label htmlFor="dedupe">Deduplicate events</Label>
                  <p className="text-xs text-muted-foreground">
                    Suppress repeats within a window. Duplicates are still stored, but deliver nowhere.
                  </p>
                </div>
                <Switch
                  id="dedupe"
                  checked={form.dedupe_enabled}
                  onCheckedChange={(checked) => setForm({ ...form, dedupe_enabled: checked })}
                />
              </div>

              {form.dedupe_enabled && (
                <div className="space-y-4 border-l-2 pl-4">
                  <div className="space-y-1.5">
                    <Label htmlFor="strategy">Strategy</Label>
                    <Select
                      value={form.dedupe_strategy}
                      onValueChange={(value) => {
                        if (value === "exact" || value === "field") {
                          setForm({ ...form, dedupe_strategy: value });
                        }
                      }}
                    >
                      <SelectTrigger id="strategy">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="exact">Exact body match</SelectItem>
                        <SelectItem value="field">Field in the body</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>

                  {form.dedupe_strategy === "field" && (
                    <div className="space-y-1.5">
                      <Label htmlFor="field-path">Field path</Label>
                      <Input
                        id="field-path"
                        value={form.dedupe_field_path}
                        onChange={(e) => setForm({ ...form, dedupe_field_path: e.target.value })}
                        placeholder="$.id"
                      />
                    </div>
                  )}

                  <div className="space-y-1.5">
                    <Label htmlFor="window">Window (seconds)</Label>
                    <Input
                      id="window"
                      type="number"
                      min={1}
                      value={form.dedupe_window_seconds}
                      onChange={(e) => setForm({ ...form, dedupe_window_seconds: e.target.value })}
                    />
                  </div>
                </div>
              )}

              {createSource.isError && (
                <p role="alert" className="text-sm text-destructive">
                  {createSource.error.message}
                </p>
              )}
            </div>

            <DialogFooter>
              <Button type="submit" disabled={!canSubmit || createSource.isPending}>
                {createSource.isPending ? "Creating…" : "Create source"}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
