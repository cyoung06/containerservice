# containerservice

Launches its owner's containers on Kubernetes: any image, with volumes, a web terminal and SSH.
One owner (a Keycloak group), one namespace.

- A **container** is a StatefulSet of one pod. Stop scales it to 0, start back to 1; a pod that
  dies comes back with the same name and volumes. Anything outside volumes is lost when the pod
  is recreated.
- A **volume** is a PersistentVolumeClaim in any storage class, mounted into any number of
  containers. A **temporary folder** is an emptyDir, gone with the pod.
- Pods run with `hostUsers: false` (root inside is root of the pod's user namespace only),
  `SYS_PTRACE` and an unconfined seccomp profile (debuggers need `personality`). Never privileged.
- Kubernetes is the only record of containers and volumes. Postgres holds the SSH keys.

## Image contract

containerservice doesn't change images. An image to use with it should:

- run `sshd` on port 22 as its main process (or under one), taking the login user's keys from
  `/etc/containerservice/authorized_keys` with
  `AuthorizedKeysCommand /usr/bin/cat /etc/containerservice/authorized_keys` (and
  `AuthorizedKeysCommandUser nobody`): as `AuthorizedKeysFile` sshd refuses it, because Kubernetes
  sets the Secret volume directory's modes. The file is the
  Secret `authorized-keys`, kept equal to the keys in the console; edits reach running pods
  within a minute or so (kubelet's Secret refresh);
- carry the login user, with passwordless sudo if wanted;
- have `/bin/sh` for the web terminal, which runs `su -l $CONTAINERSERVICE_USER` if the image sets
  that variable (and runs as root), else `bash -l` if there is one, else `sh -l`, as the image's
  user;
- for a host key that stays across restarts, use `/etc/containerservice-host/ssh_host_ed25519_key`
  (`sshd -h`): each container gets its own, in the Secret `<name>-hostkey`, deleted with the
  container.

A container's identity elsewhere is an **identity** it wears: a service account (`identity-<name>-<random>`,
never reused) whose mounted token can mint tokens for that account for any audience and do nothing
else, which the images' `sa-token` does. New containers get their own by default; identities can be
made, shared between containers and deleted on their own page. "FUSE mounts" needs the node's
`containerservice-fuse` seccomp and AppArmor profiles (`host/` in the cluster repo) and the FUSE
device plugin (`cmd/fuse-device-plugin`), and can't be on with Debugging.

The images carry [sws](https://github.com/cyoung06/sws), configured (`/etc/sws/config.toml`) for
the drive, Forgejo through git-sts and Pulp: `sws whoami`, `sws drive request "…"`, `sws drive setup`
(then `ssh drive-<owner>`, `sftp`, `rsync`, `sshfs`), `sws git setup`, `sws pulp token`.

`images/` has images that do all this (Ubuntu 24.04, user `dev` with passwordless sudo, home at
`/home/dev`): `base`, `pwn` (gdb/pwndbg, pwntools, ROPgadget, one_gadget, radare2, qemu-user) and
`claude` (Claude Code, Node, gh, Go, uv). `images/build.sh` builds and pushes them to
`oci.syeyoung.dev/containers/`. Mount a volume at `/home/dev` to keep the home folder; a fresh,
empty one is filled from `/etc/skel` on first start.

## Running

See `containerservice -h`. Deploy needs `deploy/rbac.yaml` (service account, role, the headless
Service `containers`), a Postgres database, an OIDC client, and a 32-byte session key.

Tests: `go test ./...`; the store's need `CONTAINERSERVICE_TEST_DATABASE_URL` (a database they
may own).

## cs

`cs` is the command-line client: a picker of your containers to land in over SSH (in the web
console's tmux session), quick creation from presets (`~/.config/cs/presets.toml`), port
forwards and copies. It signs in with OAuth 2.0 for native apps against the server. Get it from
the [releases](https://github.com/cyoung06/containerservice/releases), or
`go install github.com/cyoung06/containerservice/cmd/cs@latest`; then `cs login` and `cs`.
`CS_ORIGIN` points it at a server other than containers.syeyoung.dev.
