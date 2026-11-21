# API

JSON over HTTPS, signed in by the session cookie. Writes need `X-Requested-With: XMLHttpRequest`
and a same-origin request. Errors are `{"detail": "..."}` with a 4xx/5xx status.

| Method & path | Body | Answer |
|---|---|---|
| `GET /api/session` | | `{username, expires, ssh: {host, port} \| null}` |
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

`Spec`: `{name, image, mounts: [{path, volume} | {path, temp: true, sizeMiB}], cpuMillis, memoryMiB}`
(`readOnly` on a volume mount; limits 0 = none). Names: 1–52 of `a-z0-9-`, starting with a letter.

`Container`: `Spec` plus `running` (should run), `state` (`stopped`, `stopping`, `pending`,
`running`, or the waiting reason such as `ImagePullBackOff`), `message`, `restarts`, `host` (the
pod's DNS name), `ip`, `createdAt`.

`Volume`: `{name, storageClass, sizeGiB, capacityGiB, phase (pending|bound|lost|deleting), usedBy, createdAt}`.
