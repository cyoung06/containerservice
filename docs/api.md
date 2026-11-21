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
| `POST /api/containers/{name}/start` \| `/stop` | | 204 |
| `DELETE /api/containers/{name}` | | 204 (volumes stay) |
| `GET /api/containers/{name}/terminal` | websocket, subprotocol `v5.channel.k8s.io` or `v4.channel.k8s.io` | Kubernetes' exec channel protocol: byte 0 = stdin, 1 = stdout, 4 = resize `{"Width":w,"Height":h}` |
| `GET /api/volumes` | | `{volumes: [Volume]}` |
| `POST /api/volumes` | `{name, storageClass, sizeGiB}` | 201 `Volume` (empty class: the cluster default) |
| `PATCH /api/volumes/{name}` | `{sizeGiB}` | 204; grow only, if the class allows it |
| `DELETE /api/volumes/{name}` | | 204; 409 while a container mounts it |
| `GET /api/storage-classes` | | `{storageClasses: [{name, default, expandable}]}` |
| `GET /api/keys` | | `{keys: [{id, fingerprint, type, label, createdAt}]}` |
| `POST /api/keys` | `{key, label}` | 201 key; label defaults to the key's comment |
| `DELETE /api/keys/{id}` | | 204 |

`Spec`: `{name, image, mounts: [{path, volume} | {path, volume, new: {storageClass, sizeGiB}} | {path, temp: true, sizeMiB}], cpuMillis, memoryMiB, diskGiB, debug}`
(`readOnly` on a volume mount; cpu/memory 0 = none; `diskGiB` 1–100, 0 = 20, caps the root
filesystem and temporary folders; `debug`: ptrace and no seccomp filter, else hardened: the
default seccomp profile, no NET_RAW/MKNOD, and the pod label-free of
`containerservice.syeyoung.dev/debug` for the network policy that keeps it from other pods). Names: 1–52 of `a-z0-9-`, starting with a letter.
A mount with `new` makes that volume before the container (409 if it exists); everything is
checked before anything is made, and volumes made stay should the container then fail. `new` is
not read back. Each container also gets the Secret `<name>-hostkey` (its SSH host key), owned by
its StatefulSet.

`Container`: `Spec` plus `running` (should run), `state` (`stopped`, `stopping`, `pending`,
`running`, or the waiting reason such as `ImagePullBackOff`), `message`, `restarts`, `host` (the
pod's DNS name), `ip`, `createdAt`.

`Volume`: `{name, storageClass, sizeGiB, capacityGiB, phase (pending|bound|lost|deleting), usedBy, createdAt}`.
