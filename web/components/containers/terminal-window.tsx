"use client";

import { useSearchParams } from "next/navigation";
import { useEffect } from "react";

import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icon";

import { ContainerStateBadge } from "./container-state";
import { useContainers } from "./containers-context";
import { Terminal } from "./terminal";

/** The popped-out terminal: the container's shell alone, full window. */
export const TerminalWindow = () => {
  const params = useSearchParams();
  const name = params.get("name") ?? "";
  const { containers, error, refresh } = useContainers();
  const container = containers?.find((item) => item.name === name) ?? null;

  useEffect(() => {
    document.title = `${name} — terminal`;
  }, [name]);

  return (
    <div className="flex h-full min-h-0 flex-1 flex-col bg-background">
      <div className="flex h-10 shrink-0 items-center gap-2 border-b border-border px-3 text-[13px]">
        <Icon name="terminal" size={14} className="text-foreground-tertiary" />
        <span className="truncate font-medium text-foreground">{name}</span>
        {container ? <ContainerStateBadge container={container} /> : null}
      </div>
      {containers === null ? (
        <div className="flex flex-1 flex-col items-center justify-center gap-3 px-4 text-center text-xs text-foreground-tertiary">
          {error ? <p className="text-[13px] text-danger">{error}</p> : "Loading…"}
        </div>
      ) : !container ? (
        <div className="flex flex-1 items-center justify-center text-[13px] text-foreground-secondary">There is no container {name}.</div>
      ) : container.state !== "running" ? (
        <div className="flex flex-1 flex-col items-center justify-center gap-3 px-4 text-center">
          <p className="text-[13px] text-foreground-secondary">The container isn&apos;t running.</p>
          <Button onClick={() => void refresh()} leadingIcon={<Icon name="refresh" size={12} />}>
            Check again
          </Button>
        </div>
      ) : (
        <div className="flex min-h-0 flex-1 flex-col">
          <Terminal fill name={name} />
        </div>
      )}
    </div>
  );
};
