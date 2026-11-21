import { Suspense } from "react";

import { ContainersProvider } from "@/components/containers/containers-context";
import { TerminalWindow } from "@/components/containers/terminal-window";
import { SessionGate } from "@/components/session/session-gate";

/** A container's terminal alone, full window: where "Pop out" opens it. No sidebar, no app chrome. */
export default function TerminalPage() {
  return (
    <SessionGate>
      <ContainersProvider>
        <Suspense>
          <TerminalWindow />
        </Suspense>
      </ContainersProvider>
    </SessionGate>
  );
}
