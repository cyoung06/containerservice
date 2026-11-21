import type { Spec } from "@/lib/api/types";
import { formatMemory } from "@/lib/formatting/format";

/** "2 CPU · 4 GiB", "no limits", … */
export const formatLimits = (spec: Pick<Spec, "cpuMillis" | "memoryMiB" | "diskGiB">): string => {
  const parts = [
    spec.cpuMillis > 0 ? `${spec.cpuMillis / 1000} CPU` : null,
    spec.memoryMiB > 0 ? formatMemory(spec.memoryMiB) : null,
    spec.diskGiB > 0 ? `${spec.diskGiB} GiB disk` : null,
  ].filter(Boolean);
  return parts.length > 0 ? parts.join(" · ") : "no limits";
};
