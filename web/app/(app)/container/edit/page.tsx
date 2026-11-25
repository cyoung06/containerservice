"use client";

import { useSearchParams } from "next/navigation";
import { Suspense } from "react";

import { useContainers } from "@/components/containers/containers-context";
import { NewContainer } from "@/components/containers/new-container";
import { EmptyState } from "@/components/ui/empty-state";
import { Icon } from "@/components/ui/icon";

const Edit = () => {
  const name = useSearchParams().get("name") ?? "";
  const { containers } = useContainers();
  const container = containers?.find((item) => item.name === name);
  if (containers === null) {
    return <div className="px-6 py-12 text-center text-xs text-foreground-tertiary">Loading…</div>;
  }
  if (!container) {
    return <EmptyState icon={<Icon name="box" size={18} />} title="No such container" description={`There is no container ${name}.`} />;
  }
  return <NewContainer key={container.name} existing={container} />;
};

export default function EditPage() {
  return (
    <Suspense>
      <Edit />
    </Suspense>
  );
}
