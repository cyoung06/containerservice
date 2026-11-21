import { Badge, type BadgeTone } from "@/components/data/badge";
import { Icon } from "@/components/ui/icon";
import type { Container } from "@/lib/api/types";

const tones: Readonly<Record<string, BadgeTone>> = {
  running: "success",
  stopped: "neutral",
  stopping: "warning",
  pending: "info",
  ContainerCreating: "info",
  PodInitializing: "info",
  succeeded: "neutral",
};

const moving: ReadonlySet<string> = new Set(["pending", "stopping", "ContainerCreating", "PodInitializing"]);

/** The pod's state; anything not listed (ImagePullBackOff, CrashLoopBackOff, failed, …) is trouble. */
export const ContainerStateBadge = ({ container }: { readonly container: Pick<Container, "state"> }) => (
  <Badge tone={tones[container.state] ?? "danger"}>
    {moving.has(container.state) ? <Icon name="spinner" size={10} className="animate-spin" /> : null}
    {container.state}
  </Badge>
);
