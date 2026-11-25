"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useState, type ReactNode } from "react";

import { Details } from "@/components/data/details";
import { PageHeader } from "@/components/layout/page-header";
import { useSession } from "@/components/session/session-context";
import { Button } from "@/components/ui/button";
import { CopyLine } from "@/components/ui/copy-line";
import { DialogFrame } from "@/components/ui/dialog-frame";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorText } from "@/components/ui/error-text";
import { Icon } from "@/components/ui/icon";
import { TextInput } from "@/components/ui/text-input";
import { containerUrl, loadIdentities } from "@/lib/api/containers";
import { post, remove } from "@/lib/api/http";
import type { Container } from "@/lib/api/types";
import { formatTime } from "@/lib/formatting/format";
import { useAction } from "@/lib/hooks/use-action";
import { useResource } from "@/lib/hooks/use-resource";
import { editPath, sshCommand, sshConfig, terminalPath } from "@/lib/paths";

import { ContainerStateBadge } from "./container-state";
import { useContainers } from "./containers-context";
import { formatLimits } from "./limits";
import { LiveNotice } from "./live-notice";
import { Terminal } from "./terminal";

const Section = ({ title, children }: { readonly title: string; readonly children: ReactNode }) => (
  <section className="flex flex-col gap-3">
    <h2 className="text-[13px] font-medium text-foreground">{title}</h2>
    {children}
  </section>
);

const DeleteDialog = ({ container, onClose }: { readonly container: Container; readonly onClose: () => void }) => {
  const router = useRouter();
  const action = useAction();
  const [typed, setTyped] = useState("");
  const confirmed = typed === container.name;
  const identities = useResource(loadIdentities);
  const own = identities.data?.find((item) => item.name === container.identity);
  const alone = own !== undefined && own.usedBy.every((name) => name === container.name);
  const [withIdentity, setWithIdentity] = useState<boolean | null>(null);
  const deleteIdentity = (withIdentity ?? true) && alone;
  return (
    <DialogFrame
      title={`Delete ${container.name}?`}
      onClose={onClose}
      submitLabel="Delete container"
      danger
      submitDisabled={!confirmed}
      pending={action.pending}
      error={action.error}
      onSubmit={() => {
        if (confirmed) {
          void action.run(() => remove(containerUrl(container.name) + (deleteIdentity ? "?identity=delete" : ""))).then((done) => {
            if (done) {
              router.push("/");
            }
          });
        }
      }}
    >
      <p className="text-[13px] text-foreground-secondary">
        The container and everything in it outside its volumes are gone. Its volumes stay. This can&apos;t be undone.
      </p>
      <label className="mt-3 flex flex-col gap-1.5 text-[13px] text-foreground-secondary" htmlFor="delete-confirm">
        <span>
          Type <span className="font-mono text-foreground">{container.name}</span> to confirm.
        </span>
        <TextInput
          id="delete-confirm"
          data-autofocus=""
          value={typed}
          onChange={(event) => setTyped(event.target.value)}
          autoComplete="off"
          spellCheck={false}
        />
      </label>
      {container.identity ? (
        <label className="mt-3 flex items-start gap-2 text-[13px] text-foreground-secondary">
          <input type="checkbox" className="mt-0.5" checked={deleteIdentity} disabled={!alone} onChange={(event) => setWithIdentity(event.target.checked)} />
          <span>
            Also delete its identity <span className="font-medium text-foreground">{container.identity}</span>
            {alone
              ? " (what was granted to it goes with it; keep it to give its access to a later container)"
              : ` (also worn by ${own?.usedBy.filter((name) => name !== container.name).join(", ") ?? "other containers"}, so it stays)`}
          </span>
        </label>
      ) : null}
    </DialogFrame>
  );
};

export const ContainerDetail = () => {
  const router = useRouter();
  const name = useSearchParams().get("name") ?? "";
  const { containers, refresh } = useContainers();
  const { user } = useSession();
  const action = useAction();
  const [deleting, setDeleting] = useState(false);
  const container = containers?.find((item) => item.name === name) ?? null;
  const crumbs = [
    <Link key="list" href="/" className="hover:text-foreground">
      Containers
    </Link>,
  ];

  if (containers === null) {
    return <div className="px-6 py-12 text-center text-xs text-foreground-tertiary">Loading…</div>;
  }
  if (!container) {
    return (
      <>
        <PageHeader title={name} breadcrumbs={crumbs} />
        <EmptyState icon={<Icon name="box" size={18} />} title="No such container" description={`There is no container ${name}.`} />
      </>
    );
  }

  const setRunning = (running: boolean) =>
    void action.run(() => post(containerUrl(container.name, running ? "start" : "stop"))).then(() => refresh());

  const actions = (
    <>
      {container.running ? (
        <Button pending={action.pending} leadingIcon={<Icon name="square" size={12} />} onClick={() => setRunning(false)}>
          Stop
        </Button>
      ) : (
        <Button pending={action.pending} leadingIcon={<Icon name="play" size={12} />} onClick={() => setRunning(true)}>
          Start
        </Button>
      )}
      <Button leadingIcon={<Icon name="settings" size={12} />} onClick={() => router.push(editPath(container.name))}>
        Edit
      </Button>
      <Button variant="ghost" leadingIcon={<Icon name="trash" size={12} />} className="hover:text-danger" onClick={() => setDeleting(true)}>
        Delete
      </Button>
    </>
  );

  return (
    <>
      <PageHeader title={container.name} icon={<Icon name="box" size={15} />} breadcrumbs={crumbs} actions={actions} />
      <div className="scrollbar-thin min-h-0 min-w-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex max-w-[1080px] flex-col gap-6 px-4 py-5 md:px-8 md:py-6">
          <LiveNotice />
          <ErrorText message={action.error} />
          {container.message ? (
            <p className="rounded-md border border-border bg-background-secondary px-3 py-2 font-mono text-xs text-foreground-secondary [overflow-wrap:anywhere]">
              {container.message}
            </p>
          ) : null}

          <Section title="Terminal">
            {container.state === "running" ? (
              <Terminal
                name={container.name}
                actions={
                  <Button
                    size="small"
                    variant="ghost"
                    leadingIcon={<Icon name="external" size={12} />}
                    onClick={() => window.open(terminalPath(container.name), "_blank", "noopener")}
                  >
                    Pop out
                  </Button>
                }
              />
            ) : (
              <p className="rounded-lg border border-border bg-background-secondary px-4 py-6 text-center text-xs text-foreground-tertiary">
                The terminal opens once the container is running.
              </p>
            )}
          </Section>

          <Section title="Details">
            <Details
              items={[
                ["State", <ContainerStateBadge key="state" container={container} />],
                ["Image", <span key="image" className="font-mono text-xs">{container.image}</span>],
                ["Limits", formatLimits(container)],
                [
                  "Security",
                  container.debug
                    ? "Debugging: ptrace, no seccomp filter"
                    : "Hardened: default seccomp profile, no raw sockets, no traffic to other containers",
                ],
                ["Claude login", container.claudeLogin ? "Shared token mounted" : null],
                ["FUSE mounts", container.fuseMounts ? "Allowed (/dev/fuse, namespaced SYS_ADMIN)" : null],
                [
                  "Identity",
                  container.identity ? (
                    <span key="identity" className="flex flex-col gap-0.5">
                      <span>{container.identity}</span>
                      <span className="font-mono text-xs text-foreground-tertiary [overflow-wrap:anywhere]">{container.subject}</span>
                    </span>
                  ) : (
                    "None (no Kubernetes token)"
                  ),
                ],
                ["Restarts", container.restarts > 0 ? String(container.restarts) : null],
                ["Address", <span key="host" className="font-mono text-xs">{container.host}{container.ip ? ` (${container.ip})` : ""}</span>],
                ["Created", formatTime(container.createdAt)],
              ]}
            />
          </Section>

          <Section title="Mounts">
            {container.mounts.length === 0 ? (
              <p className="text-xs text-foreground-tertiary">None: everything is in the container and goes when its pod does.</p>
            ) : (
              <ul className="flex flex-col divide-y divide-border rounded-lg border border-border">
                {container.mounts.map((mount) => (
                  <li key={mount.path} className="flex items-center gap-3 px-4 py-2.5 text-[13px]">
                    <Icon name={mount.temp ? "clock" : "hard-drive"} size={13} className="shrink-0 text-foreground-tertiary" />
                    <span className="min-w-0 flex-1 truncate font-mono text-xs text-foreground">{mount.path}</span>
                    <span className="text-xs text-foreground-tertiary">
                      {mount.temp
                        ? `temporary${mount.sizeMiB ? `, up to ${mount.sizeMiB} MiB` : ""}`
                        : `volume ${mount.volume}${mount.readOnly ? ", read-only" : ""}`}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </Section>

          {user.ssh ? (
            <Section title="SSH">
              <p className="text-xs text-foreground-tertiary">
                Through the jump host, with one of your keys. The user is <code>dev</code> in the images from images/; other
                images have their own.
              </p>
              <CopyLine text={sshCommand(user.ssh, "dev", container.name)} />
              <p className="text-xs text-foreground-tertiary">
                Or in <code>~/.ssh/config</code>, for <code>ssh {container.name}</code> and editors&apos; remote SSH:
              </p>
              <CopyLine text={sshConfig(user.ssh, "dev", container.name)} block />
            </Section>
          ) : null}
        </div>
      </div>
      {deleting ? <DeleteDialog container={container} onClose={() => setDeleting(false)} /> : null}
    </>
  );
};
