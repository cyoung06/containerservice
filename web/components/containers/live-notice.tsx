"use client";

import { Button } from "@/components/ui/button";
import { ErrorText } from "@/components/ui/error-text";
import { Icon } from "@/components/ui/icon";

import { useContainers } from "./containers-context";

/** Shown while live updates are stopped (with a button to start them again) or the lists can't be read. */
export const LiveNotice = () => {
  const { stopped, reconnect, error } = useContainers();
  return (
    <>
      <ErrorText message={error} />
      {stopped ? (
        <div
          role="status"
          className="flex items-center gap-3 rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning"
        >
          <Icon name="warning" size={14} className="shrink-0" />
          <span className="min-w-0 flex-1 [overflow-wrap:anywhere]">{stopped}</span>
          <Button size="small" onClick={reconnect} leadingIcon={<Icon name="refresh" size={12} />}>
            Reconnect
          </Button>
        </div>
      ) : null}
    </>
  );
};
