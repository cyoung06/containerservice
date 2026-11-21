/** The shapes of containerservice's API (docs/api.md). */

export type SessionInfo = {
  readonly username: string;
  readonly expires: string;
  readonly ssh: { readonly host: string; readonly port: number } | null;
};

/** A volume (by name) or a temporary folder at path. */
export type Mount = {
  readonly path: string;
  readonly volume?: string;
  readonly temp?: boolean;
  readonly readOnly?: boolean;
  /** Caps a temporary folder; 0 or absent: no cap. */
  readonly sizeMiB?: number;
};

export type Spec = {
  readonly name: string;
  readonly image: string;
  readonly mounts: readonly Mount[];
  /** Limits; 0: none. */
  readonly cpuMillis: number;
  readonly memoryMiB: number;
};

export type Container = Spec & {
  /** It should run (1 replica); the pod's own state is `state`. */
  readonly running: boolean;
  /** "stopped", "stopping", "pending", "running", or why it waits (ImagePullBackOff, CrashLoopBackOff, …). */
  readonly state: string;
  readonly message?: string;
  readonly restarts: number;
  /** The pod's DNS name. */
  readonly host: string;
  readonly ip?: string;
  readonly createdAt: string;
};

export type VolumePhase = "pending" | "bound" | "lost" | "deleting";

export type Volume = {
  readonly name: string;
  readonly storageClass: string;
  readonly sizeGiB: number;
  readonly capacityGiB: number;
  readonly phase: VolumePhase;
  readonly usedBy: readonly string[];
  readonly createdAt: string;
};

export type StorageClass = { readonly name: string; readonly default: boolean; readonly expandable: boolean };

export type SshKey = {
  readonly id: string;
  readonly fingerprint: string;
  readonly type: string;
  readonly label: string;
  readonly createdAt: string;
};

export type NewKeyRequest = { readonly key: string; readonly label: string };
