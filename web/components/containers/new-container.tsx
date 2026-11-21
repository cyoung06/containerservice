"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";

import { FormField } from "@/components/form/form-field";
import { SelectField } from "@/components/form/select-field";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { ErrorText } from "@/components/ui/error-text";
import { Icon } from "@/components/ui/icon";
import { TextInput } from "@/components/ui/text-input";
import { loadStorageClasses } from "@/lib/api/containers";
import { post } from "@/lib/api/http";
import type { Container, Mount, Spec } from "@/lib/api/types";
import { useAction } from "@/lib/hooks/use-action";
import { useResource } from "@/lib/hooks/use-resource";
import { containerPath } from "@/lib/paths";

import { useSession } from "@/components/session/session-context";

import { useContainers } from "./containers-context";

/** What the form holds for one mount; numbers stay text until submitted. */
type MountRow = {
  readonly key: number;
  readonly kind: "volume" | "new" | "temp";
  readonly path: string;
  readonly volume: string;
  /** A new volume's class (null: the default) and size. */
  readonly storageClass: string | null;
  readonly sizeGiB: string;
  readonly readOnly: boolean;
  readonly sizeMiB: string;
};

const nameRule = /^[a-z]([a-z0-9-]{0,50}[a-z0-9])?$/;
const volumeRule = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;
const maxGiB = 16 << 10;
const reservedPaths = ["/etc/containerservice", "/etc/containerservice-host"];

const mountProblem = (row: MountRow, rows: readonly MountRow[]): string | null => {
  const path = row.path.trim();
  if (!path.startsWith("/") || path === "/" || path.endsWith("/") || path.includes("//") || /(^|\/)\.\.?(\/|$)/.test(path)) {
    return "An absolute, clean path other than /.";
  }
  const reserved = reservedPaths.find((keys) => path === keys || path.startsWith(`${keys}/`) || keys.startsWith(`${path}/`));
  if (reserved) {
    return `Overlaps ${reserved}, where the SSH keys go.`;
  }
  if (rows.filter((other) => other.path.trim() === path).length > 1) {
    return "Listed twice.";
  }
  if (row.kind === "volume" && !row.volume) {
    return "Pick a volume.";
  }
  if (row.kind === "new") {
    if (!volumeRule.test(row.volume)) {
      return "A volume name: 1–63 lowercase letters, digits and dashes.";
    }
    if (rows.filter((other) => other.kind === "new" && other.volume === row.volume).length > 1) {
      return "That new volume is listed twice.";
    }
    const size = Number(row.sizeGiB);
    if (!Number.isInteger(size) || size < 1 || size > maxGiB) {
      return `A size of 1–${maxGiB} GiB.`;
    }
  }
  if (row.kind === "temp" && row.sizeMiB.trim() !== "" && !(Number(row.sizeMiB) > 0)) {
    return "Empty (no cap) or a positive number of MiB.";
  }
  return null;
};

export const NewContainer = () => {
  const router = useRouter();
  const { volumes, refresh } = useContainers();
  const { user } = useSession();
  const classes = useResource(loadStorageClasses);
  const defaultClass = classes.data?.find((item) => item.default)?.name ?? classes.data?.[0]?.name ?? "";
  const action = useAction();
  const [name, setName] = useState("");
  const [image, setImage] = useState("");
  const [cpu, setCpu] = useState("");
  const [memory, setMemory] = useState("");
  const [rows, setRows] = useState<readonly MountRow[]>([]);
  const [nextKey, setNextKey] = useState(0);

  const cpuValue = cpu.trim() === "" ? 0 : Number(cpu);
  const memoryValue = memory.trim() === "" ? 0 : Number(memory);
  const nameOk = nameRule.test(name);
  const imageOk = /^[A-Za-z0-9][A-Za-z0-9._/:@-]*$/.test(image.trim());
  const cpuOk = Number.isFinite(cpuValue) && cpuValue >= 0;
  const memoryOk = Number.isInteger(memoryValue) && (memoryValue === 0 || memoryValue >= 64);
  const problems = rows.map((row) => {
    const problem = mountProblem(row, rows);
    if (problem === null && row.kind === "new" && volumes?.some((volume) => volume.name === row.volume)) {
      return "A volume with that name exists; pick it as an existing volume instead.";
    }
    return problem;
  });
  const ready = nameOk && imageOk && cpuOk && memoryOk && problems.every((problem) => problem === null);

  const update = (key: number, change: Partial<MountRow>) =>
    setRows((previous) => previous.map((row) => (row.key === key ? { ...row, ...change } : row)));
  const add = (kind: MountRow["kind"]) => {
    setRows((previous) => [
      ...previous,
      {
        key: nextKey,
        kind,
        path: kind === "temp" ? "/scratch" : kind === "new" ? "/home/dev" : "/data",
        volume: kind === "volume" ? (volumes?.[0]?.name ?? "") : kind === "new" && name ? `${name}-home` : "",
        storageClass: null,
        sizeGiB: "20",
        readOnly: false,
        sizeMiB: "",
      },
    ]);
    setNextKey((value) => value + 1);
  };

  const submit = () => {
    if (!ready) {
      return;
    }
    const mounts: Mount[] = rows.map((row) =>
      row.kind === "temp"
        ? { path: row.path.trim(), temp: true, sizeMiB: row.sizeMiB.trim() === "" ? 0 : Number(row.sizeMiB) }
        : row.kind === "new"
          ? { path: row.path.trim(), volume: row.volume, new: { storageClass: row.storageClass ?? defaultClass, sizeGiB: Number(row.sizeGiB) } }
          : { path: row.path.trim(), volume: row.volume, readOnly: row.readOnly },
    );
    const spec: Spec = { name, image: image.trim(), mounts, cpuMillis: Math.round(cpuValue * 1000), memoryMiB: memoryValue };
    void action.run(async () => {
      const created = await post<Container>("/api/containers", spec);
      await refresh();
      router.push(containerPath(created.name));
    });
  };

  const volumeItems = (volumes ?? []).map((volume) => ({
    value: volume.name,
    label: volume.name,
    description: `${volume.sizeGiB} GiB${volume.usedBy.length ? ` · used by ${volume.usedBy.join(", ")}` : ""}`,
  }));

  return (
    <>
      <PageHeader title="New container" icon={<Icon name="plus" size={15} />} />
      <div className="scrollbar-thin min-h-0 min-w-0 flex-1 overflow-y-auto">
        <form
          className="mx-auto flex max-w-[720px] flex-col gap-6 px-4 py-5 md:px-8 md:py-8"
          onSubmit={(event) => {
            event.preventDefault();
            submit();
          }}
        >
          <FormField label="Name" htmlFor="name" hint="1–52 lowercase letters, digits and dashes, starting with a letter. It's also the SSH host name.">
            <TextInput
              id="name"
              data-autofocus=""
              value={name}
              onChange={(event) => setName(event.target.value.toLowerCase())}
              placeholder="pwn"
              autoComplete="off"
              spellCheck={false}
              aria-invalid={(name !== "" && !nameOk) || undefined}
            />
          </FormField>
          <FormField
            label="Image"
            htmlFor="image"
            hint="Any image reference. For SSH it must run sshd reading /etc/containerservice/authorized_keys."
          >
            <TextInput
              id="image"
              value={image}
              onChange={(event) => setImage(event.target.value)}
              placeholder={user.images[0] ?? "docker.io/library/ubuntu:24.04"}
              autoComplete="off"
              spellCheck={false}
              className="font-mono text-xs"
            />
            {user.images.length > 0 ? (
              <div className="flex flex-wrap gap-1.5 pt-1">
                {user.images.map((ref) => (
                  <Button key={ref} size="small" variant={image.trim() === ref ? "primary" : "secondary"} onClick={() => setImage(ref)} className="font-mono">
                    {ref.split("/").pop()}
                  </Button>
                ))}
              </div>
            ) : null}
          </FormField>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <FormField label="CPU limit" htmlFor="cpu" hint="Cores, e.g. 2 or 0.5; empty: none.">
              <TextInput id="cpu" inputMode="decimal" value={cpu} onChange={(event) => setCpu(event.target.value)} placeholder="none" aria-invalid={!cpuOk || undefined} />
            </FormField>
            <FormField label="Memory limit" htmlFor="memory" hint="MiB, at least 64; empty: none.">
              <TextInput id="memory" inputMode="numeric" value={memory} onChange={(event) => setMemory(event.target.value)} placeholder="none" aria-invalid={!memoryOk || undefined} />
            </FormField>
          </div>

          <section className="flex flex-col gap-3">
            <div className="flex flex-col gap-1">
              <h2 className="text-xs font-medium text-foreground-secondary">Mounts</h2>
              <p className="text-xs text-foreground-tertiary">
                Everything outside these is lost when the pod is recreated or stopped. A new volume is made with the container and outlives it; the images keep the home folder at /home/dev.
              </p>
            </div>
            {rows.map((row, index) => (
              <div key={row.key} className="flex flex-col gap-3 rounded-lg border border-border bg-surface p-3">
                <div className="flex items-center gap-2 text-xs text-foreground-secondary">
                  <Icon name={row.kind === "temp" ? "clock" : "hard-drive"} size={13} className="text-foreground-tertiary" />
                  <span className="flex-1">{row.kind === "temp" ? "Temporary folder" : row.kind === "new" ? "New volume" : "Existing volume"}</span>
                  <Button size="small" variant="ghost" leadingIcon={<Icon name="close" size={12} />} onClick={() => setRows((previous) => previous.filter((other) => other.key !== row.key))}>
                    Remove
                  </Button>
                </div>
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                  <FormField label="Path" htmlFor={`path-${row.key}`}>
                    <TextInput id={`path-${row.key}`} value={row.path} onChange={(event) => update(row.key, { path: event.target.value })} className="font-mono text-xs" spellCheck={false} />
                  </FormField>
                  {row.kind === "volume" ? (
                    volumeItems.length > 0 ? (
                      <FormField label="Volume">
                        <SelectField label="Volume" items={volumeItems} value={row.volume} onChange={(volume) => update(row.key, { volume })} searchable />
                      </FormField>
                    ) : (
                      <p className="self-end text-xs text-foreground-tertiary">No volumes yet: make one on the Volumes page.</p>
                    )
                  ) : row.kind === "new" ? (
                    <FormField label="Volume name" htmlFor={`volume-${row.key}`}>
                      <TextInput id={`volume-${row.key}`} value={row.volume} onChange={(event) => update(row.key, { volume: event.target.value.toLowerCase() })} placeholder="home" autoComplete="off" spellCheck={false} />
                    </FormField>
                  ) : (
                    <FormField label="Size cap (MiB)" htmlFor={`size-${row.key}`}>
                      <TextInput id={`size-${row.key}`} inputMode="numeric" value={row.sizeMiB} onChange={(event) => update(row.key, { sizeMiB: event.target.value })} placeholder="none" />
                    </FormField>
                  )}
                </div>
                {row.kind === "new" ? (
                  <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                    <FormField label="Size (GiB)" htmlFor={`gib-${row.key}`}>
                      <TextInput id={`gib-${row.key}`} inputMode="numeric" value={row.sizeGiB} onChange={(event) => update(row.key, { sizeGiB: event.target.value })} />
                    </FormField>
                    {classes.data && classes.data.length > 0 ? (
                      <FormField label="Storage class">
                        <SelectField
                          label="Storage class"
                          value={row.storageClass ?? defaultClass}
                          onChange={(storageClass) => update(row.key, { storageClass })}
                          items={classes.data.map((item) => ({
                            value: item.name,
                            label: item.name,
                            description: [item.default ? "default" : null, item.expandable ? "can grow" : "fixed size"].filter(Boolean).join(" · "),
                          }))}
                        />
                      </FormField>
                    ) : (
                      <ErrorText message={classes.error} />
                    )}
                  </div>
                ) : null}
                {row.kind === "volume" ? (
                  <label className="flex items-center gap-2 text-xs text-foreground-secondary">
                    <input type="checkbox" checked={row.readOnly} onChange={(event) => update(row.key, { readOnly: event.target.checked })} />
                    Read-only
                  </label>
                ) : null}
                {problems[index] && row.path !== "" ? <p className="text-xs text-danger">{problems[index]}</p> : null}
              </div>
            ))}
            <div className="flex flex-wrap gap-2">
              <Button leadingIcon={<Icon name="plus" size={12} />} onClick={() => add("new")}>
                Add new volume
              </Button>
              <Button leadingIcon={<Icon name="hard-drive" size={12} />} onClick={() => add("volume")}>
                Add existing volume
              </Button>
              <Button leadingIcon={<Icon name="clock" size={12} />} onClick={() => add("temp")}>
                Add temporary folder
              </Button>
            </div>
          </section>

          <div className="flex flex-wrap items-center gap-3 border-t border-border pt-4">
            <Button type="submit" variant="primary" disabled={!ready} pending={action.pending} leadingIcon={<Icon name="play" size={12} />}>
              Launch
            </Button>
            <ErrorText message={action.error} />
          </div>
        </form>
      </div>
    </>
  );
};
