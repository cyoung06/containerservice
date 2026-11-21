import { get } from "./http";
import type { SshKey } from "./types";

/** The owner's SSH keys, oldest first. */
export const loadKeys = (): Promise<SshKey[]> => get<{ keys: SshKey[] }>("/api/keys").then((body) => body.keys);
