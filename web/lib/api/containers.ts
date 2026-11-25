import { get } from "./http";
import type { Container, Identity, StorageClass, Volume } from "./types";

export const loadContainers = (): Promise<Container[]> =>
  get<{ containers: Container[] }>("/api/containers").then((body) => body.containers);

export const loadIdentities = (): Promise<Identity[]> =>
  get<{ identities: Identity[] }>("/api/identities").then((body) => body.identities);

export const loadVolumes = (): Promise<Volume[]> => get<{ volumes: Volume[] }>("/api/volumes").then((body) => body.volumes);

export const loadStorageClasses = (): Promise<StorageClass[]> =>
  get<{ storageClasses: StorageClass[] }>("/api/storage-classes").then((body) => body.storageClasses);

export const containerUrl = (name: string, action = ""): string =>
  `/api/containers/${encodeURIComponent(name)}${action ? `/${action}` : ""}`;
