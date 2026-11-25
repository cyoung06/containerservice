"use client";

import { useState } from "react";

import { FormField } from "@/components/form/form-field";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { CopyLine } from "@/components/ui/copy-line";
import { ErrorText } from "@/components/ui/error-text";
import { Icon } from "@/components/ui/icon";
import { TextInput } from "@/components/ui/text-input";
import { loadIdentities } from "@/lib/api/containers";
import { post, remove } from "@/lib/api/http";
import type { Identity } from "@/lib/api/types";
import { formatTime } from "@/lib/formatting/format";
import { useAction } from "@/lib/hooks/use-action";
import { useResource } from "@/lib/hooks/use-resource";

const nameRule = /^[a-z]([a-z0-9-]{0,50}[a-z0-9])?$/;

const NewIdentity = ({ onDone }: { readonly onDone: () => Promise<void> }) => {
  const action = useAction();
  const [name, setName] = useState("");
  const ready = nameRule.test(name);
  return (
    <form
      className="flex flex-col gap-3 rounded-lg border border-border bg-surface p-4 sm:flex-row sm:items-end"
      onSubmit={(event) => {
        event.preventDefault();
        if (ready) {
          void action.run(() => post("/api/identities", { name })).then(async (done) => {
            if (done) {
              setName("");
              await onDone();
            }
          });
        }
      }}
    >
      <div className="min-w-0 flex-1">
        <FormField label="New identity" htmlFor="identity-new" hint="Lowercase letters, digits and dashes. Containers get one of their own by default; make one here to share.">
          <TextInput id="identity-new" value={name} onChange={(event) => setName(event.target.value.toLowerCase())} placeholder="agent" autoComplete="off" spellCheck={false} />
        </FormField>
      </div>
      <Button type="submit" variant="primary" disabled={!ready} pending={action.pending} leadingIcon={<Icon name="plus" size={12} />}>
        Make
      </Button>
      <ErrorText message={action.error} />
    </form>
  );
};

const Row = ({ identity, onDone }: { readonly identity: Identity; readonly onDone: () => Promise<void> }) => {
  const action = useAction();
  const worn = identity.usedBy.length > 0;
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-border bg-surface p-4">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium text-foreground">{identity.name}</span>
        <span className="text-xs text-foreground-tertiary">
          {worn ? `worn by ${identity.usedBy.join(", ")}` : "worn by nothing"} · made {formatTime(identity.createdAt)}
        </span>
        <span className="flex-1" />
        <Button
          size="small"
          variant="ghost"
          className="hover:text-danger"
          disabled={worn}
          title={worn ? "Change or delete the containers that wear it first" : undefined}
          pending={action.pending}
          leadingIcon={<Icon name="trash" size={12} />}
          onClick={() => void action.run(() => remove(`/api/identities/${encodeURIComponent(identity.name)}`)).then(() => onDone())}
        >
          Delete
        </Button>
      </div>
      <p className="text-xs text-foreground-tertiary">Subject, to grant to (the drive&apos;s workload grants and the like):</p>
      <CopyLine text={identity.subject} />
      <ErrorText message={action.error} />
    </div>
  );
};

export const IdentitiesPage = () => {
  const identities = useResource(loadIdentities);
  return (
    <>
      <PageHeader title="Identities" icon={<Icon name="user" size={15} />} />
      <div className="scrollbar-thin min-h-0 min-w-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex max-w-[720px] flex-col gap-4 px-4 py-5 md:px-8 md:py-8">
          <p className="text-xs text-foreground-tertiary">
            An identity is what a container is to other services: inside it, <code>sa-token AUDIENCE</code> prints a
            token with the identity&apos;s subject. A subject is never used again: an identity deleted and made again
            under the same name is another one, and what was granted to the old one doesn&apos;t carry over.
          </p>
          <NewIdentity onDone={identities.reload} />
          <ErrorText message={identities.error} />
          {identities.data?.length === 0 ? <p className="text-xs text-foreground-tertiary">No identities yet.</p> : null}
          {(identities.data ?? []).map((identity) => (
            <Row key={identity.name} identity={identity} onDone={identities.reload} />
          ))}
        </div>
      </div>
    </>
  );
};
