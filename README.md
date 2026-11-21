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

- run `sshd` on port 22 as its main process (or under one), with
  `AuthorizedKeysFile /etc/containerservice/authorized_keys` for the login user. The file is the
  Secret `authorized-keys`, kept equal to the keys in the console; edits reach running pods
  within a minute or so (kubelet's Secret refresh);
- carry the login user, with passwordless sudo if wanted;
- have `/bin/sh` for the web terminal, which runs `bash -l` if there is one, else `sh -l`, as the
  image's user.

## Running

See `containerservice -h`. Deploy needs `deploy/rbac.yaml` (service account, role, the headless
Service `containers`), a Postgres database, an OIDC client, and a 32-byte session key.

Tests: `go test ./...`; the store's need `CONTAINERSERVICE_TEST_DATABASE_URL` (a database they
may own).
