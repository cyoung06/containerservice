"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";

import { Cell, DataTable, Row } from "@/components/data/data-table";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Icon } from "@/components/ui/icon";
import { formatTime } from "@/lib/formatting/format";
import { containerPath } from "@/lib/paths";

import { ContainerStateBadge } from "./container-state";
import { useContainers } from "./containers-context";
import { formatLimits } from "./limits";
import { LiveNotice } from "./live-notice";

const columns = [
  { heading: "Name" },
  { heading: "State", className: "w-[170px]" },
  { heading: "Image", className: "hidden md:table-cell" },
  { heading: "Limits", className: "hidden sm:table-cell" },
  { heading: "Created", className: "hidden lg:table-cell w-[180px]" },
] as const;

export const ContainerList = () => {
  const { containers } = useContainers();
  const router = useRouter();

  const newButton = (
    <Button variant="primary" leadingIcon={<Icon name="plus" size={13} />} onClick={() => router.push("/new")}>
      New container
    </Button>
  );

  return (
    <>
      <PageHeader title="Containers" icon={<Icon name="box" size={15} />} actions={newButton} />
      <div className="scrollbar-thin min-h-0 min-w-0 flex-1 overflow-y-auto">
        <div className="flex flex-col gap-4 px-4 py-4 empty:hidden md:px-6">
          <LiveNotice />
        </div>
        {containers === null ? (
          <div className="px-6 py-12 text-center text-xs text-foreground-tertiary">Loading…</div>
        ) : containers.length === 0 ? (
          <EmptyState
            icon={<Icon name="box" size={18} />}
            title="No containers yet"
            description="Launch one from any image; it keeps running until you stop it."
            action={<div className="mt-2">{newButton}</div>}
          />
        ) : (
          <div className="overflow-x-auto">
            <DataTable columns={columns} minWidth="min-w-0">
              {containers.map((container) => (
                <Row key={container.name} onClick={() => router.push(containerPath(container.name))}>
                  <Cell>
                    <Link
                      href={containerPath(container.name)}
                      onClick={(event) => event.stopPropagation()}
                      className="font-medium text-foreground hover:underline [overflow-wrap:anywhere]"
                    >
                      {container.name}
                    </Link>
                  </Cell>
                  <Cell>
                    <ContainerStateBadge container={container} />
                  </Cell>
                  <Cell className="hidden font-mono text-xs md:table-cell [overflow-wrap:anywhere]" muted>
                    {container.image}
                  </Cell>
                  <Cell className="hidden sm:table-cell" muted>
                    {formatLimits(container)}
                  </Cell>
                  <Cell className="hidden lg:table-cell" muted>
                    {formatTime(container.createdAt)}
                  </Cell>
                </Row>
              ))}
            </DataTable>
          </div>
        )}
      </div>
    </>
  );
};
