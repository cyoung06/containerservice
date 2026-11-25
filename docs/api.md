# API

JSON over HTTPS, signed in by the session cookie. Writes need `X-Requested-With: XMLHttpRequest`
and a same-origin request. Errors are `{"detail": "..."}` with a 4xx/5xx status.

| Method & path | Body | Answer |
|---|---|---|
| `GET /api/session` | | `{username, expires, ssh: {host, port} \| null, images: [ref]}` (images: suggestions for a new container) |
| `GET /api/events` | | server-sent events: `changed` whenever a container or volume changes |
| `GET /api/containers` | | `{containers: [Container]}` |
| `GET /api/containers/{name}` | | `Container` |
| `POST /api/containers` | `Spec` | 201 `Container`; 409 if the name is taken |
| `PATCH /api/containers/{name}` | `Spec` (the name can't change) | 200 `Container`; replaces the settings, restarting a running container's pod (volumes keep their data) |
| `POST /api/containers/{name}/start` \| `/stop` | | 204 |
| `DELETE /api/containers/{name}` | `?identity=delete`: its identity too | 204 (volumes stay; the identity only if no other container wears it, else 409 after the container is gone) |
| `GET /api/containers/{name}/terminal` | websocket, subprotocol `v5.channel.k8s.io` or `v4.channel.k8s.io` | Kubernetes' exec channel protocol: byte 0 = stdin, 1 = stdout, 4 = resize `{"Width":w,"Height":h}` |
| `GET /api/volumes` | | `{volumes: [Volume]}` |
| `POST /api/volumes` | `{name, storageClass, sizeGiB}` | 201 `Volume` (empty class: the cluster default) |
| `PATCH /api/volumes/{name}` | `{sizeGiB}` | 204; grow only, if the class allows it |
| `DELETE /api/volumes/{name}` | | 204; 409 while a container mounts it |
| `GET /api/storage-classes` | | `{storageClasses: [{name, default, expandable}]}` |
| `GET /api/identities` | | `{identities: [{name, serviceAccount, subject, usedBy, createdAt}]}` |
| `POST /api/identities` | `{name}` | 201 identity; 409 if the name is taken (at most 100) |
| `DELETE /api/identities/{name}` | | 204; 409 while a container wears it |
| `GET /api/keys` | | `{keys: [{id, fingerprint, type, label, createdAt}]}` |
| `POST /api/keys` | `{key, label}` | 201 key; label defaults to the key's comment |
| `DELETE /api/keys/{id}` | | 204 |

`Spec`: `{name, image, mounts: [{path, volume} | {path, volume, new: {storageClass, sizeGiB}} | {path, temp: true, sizeMiB}], cpuMillis, memoryMiB, diskGiB, debug, claudeLogin, fuseMounts}`
(`readOnly` on a volume mount; cpu/memory 0 = none; `diskGiB` 1–100, 0 = 20, caps the root
filesystem and temporary folders; `debug`: ptrace and no seccomp filter, else hardened: the
default seccomp profile, no NET_RAW/MKNOD, and the pod label-free of
`containerservice.syeyoung.dev/debug` for the network policy that keeps it from other pods;
`claudeLogin`: the shared Claude token; `fuseMounts`: /dev/fuse (`syeyoung.dev/fuse`), CAP_SYS_ADMIN
in its own user namespace, and the node's `containerservice-fuse` seccomp and AppArmor profiles,
which allow FUSE mounts and nothing more).

Every container wears an identity (`identity`, the name of one; a container without one is refused,
400). An identity is a service account of the namespace, `identity-<name>-<8 random characters>`,
never used again, whose mounted token may do one thing: create tokens for that account
(TokenRequest), for any audience (the images' `sa-token AUDIENCE`). Its subject for the cluster's
issuer, `system:serviceaccount:<namespace>:<account>` (`Container.subject`), is the identity other
services grant to (the drive's workload grants, for one). New containers get one of their own by
default (`newIdentity: true` with `identity` set to its name makes it); several may wear one and
share what is granted to it. An identity deleted and made again under the same name is another one.
Debugging and FUSE mounts can't both be on (400).

`Container`: `Spec` plus `running` (should run), `state` (`stopped`, `stopping`, `pending`,
`running`, or the waiting reason such as `ImagePullBackOff`), `message`, `restarts`, `host` (the
pod's DNS name), `ip`, `createdAt`.

`Volume`: `{name, storageClass, sizeGiB, capacityGiB, phase (pending|bound|lost|deleting), usedBy, createdAt}`.
