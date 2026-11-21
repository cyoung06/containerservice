import type { ReactNode } from "react";

import { ContainersProvider } from "@/components/containers/containers-context";
import { MobileTopBar } from "@/components/layout/mobile-top-bar";
import { Sidebar } from "@/components/layout/sidebar";
import { SidebarProvider } from "@/components/layout/sidebar-provider";
import { SessionGate } from "@/components/session/session-gate";

export default function AppLayout({ children }: { readonly children: ReactNode }) {
  return (
    <SessionGate>
      <ContainersProvider>
        <SidebarProvider>
          <div className="flex h-full min-h-0 flex-1 flex-col md:flex-row">
            <MobileTopBar />
            <Sidebar />
            <main className="flex min-h-0 min-w-0 flex-1 flex-col overflow-x-hidden bg-background">{children}</main>
          </div>
        </SidebarProvider>
      </ContainersProvider>
    </SessionGate>
  );
}
