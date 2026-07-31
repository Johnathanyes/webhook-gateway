import { useState, type ReactNode } from "react";

import { useDestinations } from "@/api/destinations";
import { useCreateRule, useDeleteRule, useRules, useUpdateRule, type Rule, type RuleInput } from "@/api/rules";
import { useSources } from "@/api/sources";
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";

const emptyRule: RuleInput = {
  source_id: "",
  name: "",
  expression: "",
  action: "drop",
  route_destination_ids: [],
  priority: 100,
  enabled: true,
};

export default function Rules() {
  const { data: rules, isPending, error } = useRules();
  const { data: sources } = useSources();
  const deleteRule = useDeleteRule();

  const sourceName = (id: string) => sources?.find((s) => s.id === id)?.name ?? id;

  return (
    <div className="space-y-6">
      <header className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Rules</h1>
          <p className="text-sm text-muted-foreground">
            CEL expressions evaluated per event, lowest priority first. The first match wins.
          </p>
        </div>
        <RuleDialog trigger={<Button>New rule</Button>} />
      </header>

      {isPending && <p className="text-sm text-muted-foreground">Loading…</p>}
      {error && <p className="text-sm text-destructive">{error.message}</p>}

      {rules && rules.length === 0 && <p className="text-sm text-muted-foreground">No rules yet.</p>}

      {rules && rules.length > 0 && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Priority</TableHead>
              <TableHead>Name</TableHead>
              <TableHead>Source</TableHead>
              <TableHead>Expression</TableHead>
              <TableHead>Action</TableHead>
              <TableHead>Enabled</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {[...rules]
              .sort((a, b) => a.priority - b.priority)
              .map((rule) => (
                <TableRow key={rule.id}>
                  <TableCell className="text-muted-foreground">{rule.priority}</TableCell>
                  <TableCell className="font-medium">{rule.name}</TableCell>
                  <TableCell>{sourceName(rule.source_id)}</TableCell>
                  <TableCell>
                    <code className="text-xs">{rule.expression}</code>
                  </TableCell>
                  <TableCell>
                    <Badge variant={rule.action === "drop" ? "outline" : "secondary"}>{rule.action}</Badge>
                  </TableCell>
                  <TableCell className="text-sm text-muted-foreground">
                    {rule.enabled ? "yes" : "no"}
                  </TableCell>
                  <TableCell className="space-x-2 text-right">
                    <RuleDialog
                      rule={rule}
                      trigger={
                        <Button variant="outline" size="sm">
                          Edit
                        </Button>
                      }
                    />
                    <Button variant="outline" size="sm" onClick={() => deleteRule.mutate(rule.id)}>
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

function RuleDialog({ rule, trigger }: { rule?: Rule; trigger: ReactNode }) {
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState<RuleInput>(rule ?? emptyRule);

  const { data: sources } = useSources();
  const { data: destinations } = useDestinations();
  const create = useCreateRule();
  const update = useUpdateRule();
  const mutation = rule ? update : create;

  const selectedDestinations = form.route_destination_ids ?? [];

  function toggleDestination(id: string, checked: boolean) {
    setForm({
      ...form,
      route_destination_ids: checked
        ? [...selectedDestinations, id]
        : selectedDestinations.filter((d) => d !== id),
    });
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) {
          setForm(rule ?? emptyRule);
          mutation.reset();
        }
      }}
    >
      <DialogTrigger>{trigger}</DialogTrigger>

      <DialogContent className="sm:max-w-lg">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const onSuccess = () => setOpen(false);
            if (rule) {
              update.mutate({ id: rule.id, input: form }, { onSuccess });
            } else {
              create.mutate(form, { onSuccess });
            }
          }}
        >
          <DialogHeader>
            <DialogTitle>{rule ? "Edit rule" : "New rule"}</DialogTitle>
            <DialogDescription>
              The expression is compiled when you save — an invalid one is rejected here, not at
              delivery time.
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 py-4">
            <div className="space-y-1.5">
              <Label htmlFor="rule-name">Name</Label>
              <Input
                id="rule-name"
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
                placeholder="drop test-mode events"
                autoFocus
              />
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="rule-source">Source</Label>
              <Select
                value={form.source_id}
                onValueChange={(value) => value && setForm({ ...form, source_id: value })}
              >
                <SelectTrigger id="rule-source">
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
              <Label htmlFor="expression">Expression</Label>
              <Textarea
                id="expression"
                className="font-mono text-xs"
                rows={3}
                value={form.expression}
                onChange={(e) => setForm({ ...form, expression: e.target.value })}
                placeholder="body.livemode == false"
              />
              <p className="text-xs text-muted-foreground">
                CEL over <code>body</code>, <code>headers</code>, and <code>source</code>.
              </p>
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="action">Action when it matches</Label>
              <Select
                value={form.action}
                onValueChange={(value) => {
                  if (value === "drop" || value === "route") {
                    setForm({ ...form, action: value });
                  }
                }}
              >
                <SelectTrigger id="action">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="drop">Drop the event</SelectItem>
                  <SelectItem value="route">Route to specific destinations</SelectItem>
                </SelectContent>
              </Select>
            </div>

            {form.action === "route" && (
              <div className="space-y-2 border-l-2 pl-4">
                <Label>Destinations</Label>
                {destinations?.map((destination) => (
                  <div key={destination.id} className="flex items-center gap-2">
                    <Checkbox
                      id={`dest-${destination.id}`}
                      checked={selectedDestinations.includes(destination.id)}
                      onCheckedChange={(checked) => toggleDestination(destination.id, checked === true)}
                    />
                    <Label htmlFor={`dest-${destination.id}`} className="font-normal">
                      {destination.name}
                    </Label>
                  </div>
                ))}
                <p className="text-xs text-muted-foreground">
                  These replace the destinations the event's routes would have used.
                </p>
              </div>
            )}

            <div className="grid grid-cols-2 items-end gap-4">
              <div className="space-y-1.5">
                <Label htmlFor="priority">Priority</Label>
                <Input
                  id="priority"
                  type="number"
                  value={form.priority}
                  onChange={(e) => setForm({ ...form, priority: Number(e.target.value) })}
                />
                <p className="text-xs text-muted-foreground">Lower runs first.</p>
              </div>
              <div className="flex items-center gap-2 pb-6">
                <Switch
                  id="rule-enabled"
                  checked={form.enabled}
                  onCheckedChange={(checked) => setForm({ ...form, enabled: checked })}
                />
                <Label htmlFor="rule-enabled">Enabled</Label>
              </div>
            </div>

            {mutation.isError && (
              <p role="alert" className="text-sm text-destructive">
                {mutation.error.message}
              </p>
            )}
          </div>

          <DialogFooter>
            <Button
              type="submit"
              disabled={
                form.name === "" ||
                form.source_id === "" ||
                form.expression === "" ||
                (form.action === "route" && selectedDestinations.length === 0) ||
                mutation.isPending
              }
            >
              {mutation.isPending ? "Saving…" : rule ? "Save changes" : "Create rule"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
