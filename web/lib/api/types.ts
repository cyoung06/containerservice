/** The shapes of containerservice's API (docs/api.md). */

export type SessionInfo = {
  readonly username: string;
  readonly expires: string;
  readonly ssh: { readonly host: string; readonly port: number } | null;
  /** Images the new-container form suggests. */
  readonly images: readonly string[];
};

/** A volume (by name) or a temporary folder at path. */
export type Mount = {
  readonly path: string;
  readonly volume?: string;
  readonly temp?: boolean;
  readonly readOnly?: boolean;
  /** Caps a temporary folder; 0 or absent: no cap. */
  readonly sizeMiB?: number;
  /** On a create: make the volume first (it mustn't exist). Never read back. */
  readonly new?: { readonly storageClass: string; readonly sizeGiB: number };
};

export type Spec = {
  readonly name: string;
  readonly image: string;
  readonly mounts: readonly Mount[];
  /** Limits; 0: none. */
  readonly cpuMillis: number;
  readonly memoryMiB: number;
  /** Caps the container's own disk use (root filesystem, temporary folders); 0 on a create: 20 GiB. */
  readonly diskGiB: number;
  /** Debuggers: ptrace and no seccomp filter. Off: hardened. */
  readonly debug: boolean;
  /** Mounts the shared Claude Code token (CLAUDE_CODE_OAUTH_TOKEN in the claude image). */
  readonly claudeLogin: boolean;
};

/** The Claude page: whether a token is set (it's never sent back) and the shared settings JSON. */
export type ClaudeInfo = {
  readonly tokenSet: boolean;
  readonly tokenAt: string | null;
  readonly settings: string;
  readonly settingsAt: string | null;
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
