import { useState } from "react";

import { useApiKeys, useCreateApiKey, useRevokeApiKey, type CreatedApiKey, type Scope } from "@/api/apikeys";
import CopyButton from "@/components/CopyButton";
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
import { Checkbox } from "@/components/ui/checkbox";
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
import { formatDateTime } from "@/lib/format";

// The scope vocabulary from internal/api/middleware. The hints say which CLI
// command needs which scope, because that is the question someone minting a key
// is actually trying to answer.
const scopes: { value: Scope; label: string; hint: string }[] = [
  { value: "read", label: "read", hint: "List and inspect everything. Needed by whg login." },
  { value: "write", label: "write", hint: "Create and change config. Needed by whg trigger." },
  { value: "replay", label: "replay", hint: "Re-deliver events and recover dead letters." },
  { value: "tunnel", label: "tunnel", hint: "Open a local dev tunnel. Needed by whg listen." },
];

export default function ApiKeys() {
  const { data: keys, isPending, error } = useApiKeys();
  const revoke = useRevokeApiKey();

  return (
    <div className="space-y-6">
      <header className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold">API keys</h1>
          <p className="text-sm text-muted-foreground">
            Credentials for the CLI and for programmatic access. The dashboard itself uses your login
            session, not a key.
          </p>
        </div>
        <NewApiKeyDialog />
      </header>

      {isPending && <p className="text-sm text-muted-foreground">Loading…</p>}
      {error && <p className="text-sm text-destructive">{error.message}</p>}

      {keys && keys.length === 0 && <p className="text-sm text-muted-foreground">No keys yet.</p>}

      {keys && keys.length > 0 && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Key</TableHead>
              <TableHead>Scopes</TableHead>
              <TableHead>Created</TableHead>
              <TableHead>Last used</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {keys.map((key) => (
              <TableRow key={key.id} className={key.revoked_at ? "opacity-60" : undefined}>
                <TableCell className="font-medium">{key.name}</TableCell>
                <TableCell>
                  <code className="text-xs">{key.key_prefix}…</code>
                </TableCell>
                <TableCell className="space-x-1">
                  {key.scopes.map((scope) => (
                    <Badge key={scope} variant="secondary">
                      {scope}
                    </Badge>
                  ))}
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {formatDateTime(key.created_at)}
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {formatDateTime(key.last_used_at, "never")}
                </TableCell>
                <TableCell className="text-right">
                  {key.revoked_at ? (
                    <Badge variant="outline">revoked {formatDateTime(key.revoked_at)}</Badge>
                  ) : (
                    <AlertDialog>
                      <AlertDialogTrigger>
                        <Button variant="outline" size="sm" disabled={revoke.isPending}>
                          Revoke
                        </Button>
                      </AlertDialogTrigger>
                      <AlertDialogContent>
                        <AlertDialogHeader>
                          <AlertDialogTitle>Revoke {key.name}?</AlertDialogTitle>
                          <AlertDialogDescription>
                            Anything using this key starts getting 401s immediately. This cannot be
                            undone — mint a new key instead. The row stays listed as an audit trail.
                          </AlertDialogDescription>
                        </AlertDialogHeader>
                        <AlertDialogFooter>
                          <AlertDialogCancel>Cancel</AlertDialogCancel>
                          <AlertDialogAction onClick={() => revoke.mutate(key.id)}>
                            Revoke key
                          </AlertDialogAction>
                        </AlertDialogFooter>
                      </AlertDialogContent>
                    </AlertDialog>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}

function NewApiKeyDialog() {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  // read alone is enough for `whg login` to verify the key, which is the most
  // common first thing someone does with one.
  const [selected, setSelected] = useState<Scope[]>(["read"]);
  const [created, setCreated] = useState<CreatedApiKey | null>(null);

  const createKey = useCreateApiKey();

  function reset() {
    setName("");
    setSelected(["read"]);
    setCreated(null);
    createKey.reset();
  }

  function toggleScope(scope: Scope, checked: boolean) {
    setSelected((current) =>
      checked ? [...current, scope] : current.filter((s) => s !== scope),
    );
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
        <Button>New API key</Button>
      </DialogTrigger>

      <DialogContent className="sm:max-w-lg">
        {created ? (
          <>
            <DialogHeader>
              <DialogTitle>Key created</DialogTitle>
              <DialogDescription>
                Copy it now. The gateway stores only a hash and cannot show it again.
              </DialogDescription>
            </DialogHeader>

            <div className="space-y-4">
              <div className="flex items-center gap-2">
                <code className="min-w-0 flex-1 truncate rounded-md bg-muted px-2 py-1.5 text-xs">
                  {created.key}
                </code>
                <CopyButton value={created.key} />
              </div>

              <div className="space-y-1.5">
                <Label>Use it with the CLI</Label>
                <code className="block rounded-md bg-muted px-2 py-1.5 text-xs">
                  whg login --url {window.location.origin}
                </code>
                <p className="text-xs text-muted-foreground">
                  Paste the key when prompted. Lose it and you mint a new one — there is no recovery.
                </p>
              </div>
            </div>

            <DialogFooter>
              <Button onClick={() => setOpen(false)}>Done</Button>
            </DialogFooter>
          </>
        ) : (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              createKey.mutate({ name, scopes: selected }, { onSuccess: setCreated });
            }}
          >
            <DialogHeader>
              <DialogTitle>New API key</DialogTitle>
              <DialogDescription>
                Grant the narrowest set of scopes that does the job — a key cannot be edited later.
              </DialogDescription>
            </DialogHeader>

            <div className="space-y-4 py-4">
              <div className="space-y-1.5">
                <Label htmlFor="key-name">Name</Label>
                <Input
                  id="key-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="laptop-cli"
                  autoFocus
                />
                <p className="text-xs text-muted-foreground">
                  For your own records — it shows up in this list and nowhere else.
                </p>
              </div>

              <div className="space-y-2">
                <Label>Scopes</Label>
                {scopes.map((scope) => (
                  <div key={scope.value} className="flex items-start gap-2">
                    <Checkbox
                      id={`scope-${scope.value}`}
                      className="mt-1"
                      checked={selected.includes(scope.value)}
                      onCheckedChange={(checked) => toggleScope(scope.value, checked === true)}
                    />
                    <div>
                      <Label htmlFor={`scope-${scope.value}`} className="font-mono font-normal">
                        {scope.label}
                      </Label>
                      <p className="text-xs text-muted-foreground">{scope.hint}</p>
                    </div>
                  </div>
                ))}
              </div>

              {createKey.isError && (
                <p role="alert" className="text-sm text-destructive">
                  {createKey.error.message}
                </p>
              )}
            </div>

            <DialogFooter>
              <Button type="submit" disabled={name === "" || selected.length === 0 || createKey.isPending}>
                {createKey.isPending ? "Creating…" : "Create key"}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
