/** Pages of a static export take their object from the query string. */
export const containerPath = (name: string): string => `/container/?name=${encodeURIComponent(name)}`;

/** A container's edit form. */
export const editPath = (name: string): string => `/container/edit/?name=${encodeURIComponent(name)}`;

/** The full-window terminal a container's terminal pops out into. */
export const terminalPath = (name: string): string => `/terminal/?name=${encodeURIComponent(name)}`;

type Bastion = { readonly host: string; readonly port: number };

/** The SSH command that reaches a container through the bastion. */
export const sshCommand = (ssh: Bastion, user: string, name: string): string => `ssh -J ${ssh.host}:${ssh.port} ${user}@${name}`;

/** An ~/.ssh/config entry for a container, so that plain `ssh <name>` (and editors' remote SSH) work. */
export const sshConfig = (ssh: Bastion, user: string, name: string): string =>
  [`Host ${name}`, `  HostName ${name}`, `  User ${user}`, `  ProxyJump ${ssh.host}:${ssh.port}`].join("\n");
