/** Pages of a static export take their object from the query string. */
export const containerPath = (name: string): string => `/container/?name=${encodeURIComponent(name)}`;

/** The full-window terminal a container's terminal pops out into. */
export const terminalPath = (name: string): string => `/terminal/?name=${encodeURIComponent(name)}`;

/** The SSH command that reaches a container through the bastion. */
export const sshCommand = (ssh: { readonly host: string; readonly port: number }, user: string, name: string): string =>
  `ssh -J ${ssh.host}:${ssh.port} ${user}@${name}`;
