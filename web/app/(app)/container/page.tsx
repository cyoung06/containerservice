import { Suspense } from "react";

import { ContainerDetail } from "@/components/containers/container-detail";

export default function ContainerPage() {
  return (
    <Suspense>
      <ContainerDetail />
    </Suspense>
  );
}
