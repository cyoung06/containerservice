"use client";

import { useState } from "react";

import { Badge } from "@/components/data/badge";
import { Cell, DataTable, Row } from "@/components/data/data-table";
import { FormField } from "@/components/form/form-field";
import { SelectField } from "@/components/form/select-field";
import { LiveNotice } from "@/components/containers/live-notice";
import { useContainers } from "@/components/containers/containers-context";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { DialogFrame } from "@/components/ui/dialog-frame";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorText } from "@/components/ui/error-text";
import { Icon } from "@/components/ui/icon";
import { TextInput } from "@/components/ui/text-input";
import { loadStorageClasses } from "@/lib/api/containers";
import { patch, post, remove } from "@/lib/api/http";
import type { StorageClass, Volume } from "@/lib/api/types";
import { formatTime } from "@/lib/formatting/format";
import { useAction } from "@/lib/hooks/use-action";
import { useResource } from "@/lib/hooks/use-resource";

const volumeName = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;
const maxGiB = 16 << 10;

const NewVolume = ({ classes, onDone }: { readonly classes: readonly StorageClass[]; readonly onDone: () => Promise<void> }) => {
  const action = useAction();
  const fallback = classes.find((item) => item.default)?.name ?? classes[0]?.name ?? "";
  const [name, setName] = useState("");
  const [size, setSize] = useState("10");
  const [storageClass, setStorageClass] = useState<string | null>(null);
  const chosen = storageClass ?? fallback;
  const sizeValue = Number(size);
  const ready = volumeName.test(name) && Number.isInteger(sizeValue) && sizeValue >= 1 && sizeValue <= maxGiB;
  return (
    <form
      className="flex flex-col gap-4 rounded-lg border border-border bg-surface p-4"
      onSubmit={(event) => {
        event.preventDefault();
        if (ready) {
          void action.run(() => post("/api/volumes", { name, storageClass: chosen, sizeGiB: sizeValue })).then(async (done) => {
            if (done) {
              setName("");
              await onDone();
            }
          });
        }
      }}
    >
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <FormField label="Name" htmlFor="volume-name">
          <TextInput id="volume-name" value={name} onChange={(event) => setName(event.target.value.toLowerCase())} placeholder="work" autoComplete="off" spellCheck={false} />
        </FormField>
        <FormField label="Size (GiB)" htmlFor="volume-size">
          <TextInput id="volume-size" inputMode="numeric" value={size} onChange={(event) => setSize(event.target.value)} />
        </FormField>
        <FormField label="Storage class">
          <SelectField
            label="Storage class"
            value={chosen}
            onChange={setStorageClass}
            items={classes.map((item) => ({
              value: item.name,
              label: item.name,
              description: [item.default ? "default" : null, item.expandable ? "can grow" : "fixed size"].filter(Boolean).join(" · "),
            }))}
          />
        </FormField>
      </div>
      <div className="flex flex-wrap items-center gap-3">
        <Button type="submit" variant="primary" disabled={!ready} pending={action.pending} leadingIcon={<Icon name="plus" size={12} />}>
          Create volume
        </Button>
        <ErrorText message={action.error} />
      </div>
    </form>
  );
};

const ResizeDialog = ({ volume, onClose, onDone }: { readonly volume: Volume; readonly onClose: () => void; readonly onDone: () => Promise<void> }) => {
  const action = useAction();
  const [size, setSize] = useState(String(volume.sizeGiB * 2));
  const value = Number(size);
  const ready = Number.isInteger(value) && value > volume.sizeGiB && value <= maxGiB;
  return (
    <DialogFrame
      title={`Grow ${volume.name}`}
      onClose={onClose}
      submitLabel="Grow"
      submitDisabled={!ready}
      pending={action.pending}
      error={action.error}
      onSubmit={() =>
        void action.run(() => patch(`/api/volumes/${encodeURIComponent(volume.name)}`, { sizeGiB: value })).then(async (done) => {
          if (done) {
            onClose();
            await onDone();
          }
        })
      }
    >
      <FormField label="New size (GiB)" htmlFor="resize" hint={`Now ${volume.sizeGiB} GiB. Volumes only grow, and only if their storage class allows it.`}>
        <TextInput id="resize" data-autofocus="" inputMode="numeric" value={size} onChange={(event) => setSize(event.target.value)} />
      </FormField>
    </DialogFrame>
  );
};

const DeleteDialog = ({ volume, onClose, onDone }: { readonly volume: Volume; readonly onClose: () => void; readonly onDone: () => Promise<void> }) => {
  const action = useAction();
  const [typed, setTyped] = useState("");
  const confirmed = typed === volume.name;
  return (
    <DialogFrame
      title={`Delete ${volume.name}?`}
      onClose={onClose}
      submitLabel="Delete volume"
      danger
      submitDisabled={!confirmed}
      pending={action.pending}
      error={action.error}
      onSubmit={() => {
        if (confirmed) {
          void action.run(() => remove(`/api/volumes/${encodeURIComponent(volume.name)}`)).then(async (done) => {
            if (done) {
              onClose();
              await onDone();
            }
          });
        }
      }}
    >
      <p className="text-[13px] text-foreground-secondary">Everything on it is deleted. This can&apos;t be undone.</p>
      <label className="flex flex-col gap-1.5 text-[13px] text-foreground-secondary" htmlFor="delete-volume">
        <span>
          Type <span className="font-mono text-foreground">{volume.name}</span> to confirm.
        </span>
        <TextInput id="delete-volume" data-autofocus="" value={typed} onChange={(event) => setTyped(event.target.value)} autoComplete="off" spellCheck={false} />
      </label>
    </DialogFrame>
  );
};

const columns = [
  { heading: "Name" },
  { heading: "Size", className: "w-[120px]" },
  { heading: "Class", className: "hidden sm:table-cell w-[120px]" },
  { heading: "Used by", className: "hidden md:table-cell" },
  { heading: "Created", className: "hidden lg:table-cell w-[180px]" },
  { heading: "", className: "w-[150px]" },
] as const;

export const VolumesPage = () => {
  const { volumes, refresh } = useContainers();
  const classes = useResource(loadStorageClasses);
  const [resizing, setResizing] = useState<Volume | null>(null);
  const [deleting, setDeleting] = useState<Volume | null>(null);
  const expandable = new Set((classes.data ?? []).filter((item) => item.expandable).map((item) => item.name));

  return (
    <>
      <PageHeader title="Volumes" icon={<Icon name="hard-drive" size={15} />} />
      <div className="scrollbar-thin min-h-0 min-w-0 flex-1 overflow-y-auto">
        <div className="flex flex-col gap-4 px-4 py-4 md:px-6">
          <LiveNotice />
          <ErrorText message={classes.error} />
          {classes.data ? <NewVolume classes={classes.data} onDone={refresh} /> : null}
        </div>
        {volumes === null ? (
          <div className="px-6 py-12 text-center text-xs text-foreground-tertiary">Loading…</div>
        ) : volumes.length === 0 ? (
          <EmptyState icon={<Icon name="hard-drive" size={18} />} title="No volumes yet" description="A volume keeps its files across stops, restarts and container deletes." />
        ) : (
          <div className="overflow-x-auto">
            <DataTable columns={columns} minWidth="min-w-0">
              {volumes.map((volume) => (
                <Row key={volume.name}>
                  <Cell>
                    <span className="font-medium text-foreground">{volume.name}</span>
                    {volume.phase !== "bound" ? (
                      <span className="ml-2">
                        <Badge tone={volume.phase === "lost" ? "danger" : volume.phase === "deleting" ? "warning" : "info"}>{volume.phase}</Badge>
                      </span>
                    ) : null}
                  </Cell>
                  <Cell muted>
                    {volume.sizeGiB} GiB
                    {volume.capacityGiB && volume.capacityGiB !== volume.sizeGiB ? <span className="text-foreground-quaternary"> (has {volume.capacityGiB})</span> : null}
                  </Cell>
                  <Cell className="hidden sm:table-cell" muted>
                    {volume.storageClass || "default"}
                  </Cell>
                  <Cell className="hidden md:table-cell" muted>
                    {volume.usedBy.length ? volume.usedBy.join(", ") : "—"}
                  </Cell>
                  <Cell className="hidden lg:table-cell" muted>
                    {formatTime(volume.createdAt)}
                  </Cell>
                  <Cell>
                    <div className="flex justify-end gap-1">
                      {expandable.has(volume.storageClass) ? (
                        <Button size="small" variant="ghost" onClick={() => setResizing(volume)}>
                          Grow
                        </Button>
                      ) : null}
                      <Button
                        size="small"
                        variant="ghost"
                        className="hover:text-danger"
                        disabled={volume.usedBy.length > 0}
                        title={volume.usedBy.length > 0 ? "Mounted by a container" : undefined}
                        onClick={() => setDeleting(volume)}
                      >
                        Delete
                      </Button>
                    </div>
                  </Cell>
                </Row>
              ))}
            </DataTable>
          </div>
        )}
      </div>
      {resizing ? <ResizeDialog volume={resizing} onClose={() => setResizing(null)} onDone={refresh} /> : null}
      {deleting ? <DeleteDialog volume={deleting} onClose={() => setDeleting(null)} onDone={refresh} /> : null}
    </>
  );
};
