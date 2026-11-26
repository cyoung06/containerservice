# This machine

You are running in a container of containerservice (containers.syeyoung.dev), the owner's own
container launcher on the club's Kubernetes cluster. It's a sandbox for you to work in.

- **User and root:** you run as `dev` (uid 1000), with passwordless `sudo`. Root here is root of
  the container's own user namespace only. You can install anything (`sudo apt-get install`,
  `npm`, `uv`, `go install`).
- **What persists:** a pod that is recreated (edited, restarted, moved) keeps only its volumes. Is
  `/home/dev` a volume? `findmnt /home/dev` says. Everything else, including packages installed
  with apt, is gone after a restart. Keep work in the home folder or another mounted volume.
- **The console:** the person usually watches you through the web console or `cs`, both attached
  to one tmux session. They may be away: see notifications below.
- **Network:** the internet is open. Nothing of the club's is reachable from here: no LAN, no other
  pods, no hosts. The club's services are reached through their public names, with this
  container's identity (sws, below), never with secrets copied in.
- **Limits:** CPU, memory and disk may be capped. Containers are hardened unless made with
  "Debugging": no ptrace, and a seccomp filter. FUSE mounts (sshfs, rclone mount) work only when
  the container has "FUSE mounts" on.

## sws: this container's identity

Each container wears an identity, a Kubernetes service account. `sws` turns it into access to the
club's services. No password or key is stored: a member approves a request once, on a link.
`/etc/sws/config.toml` says where each service is.

    sws whoami                         who this container is (the subject people grant to)
    sws drive request "what for"       ask a member for a drive folder: prints a link and waits
    sws drive grants                   the folders shared with this container
    sws drive setup                    once; then ssh/sftp/rsync/sshfs reach drive-<owner>
    sws git setup                      git's credential helper for git.syeyoung.dev (Forgejo)
    sws git request <owner> [-repo r] [-permission read|write|admin] "what for"
    sws pulp request <base path>:pull|push -label "what for"; sws pulp token
    sws notify request "what for"      ask a member to get this container's notifications
    sws notify send [-body text] [-link https://…] "title"
    sws notify grants                  who gets them

A request prints a link and waits until a member answers it. Give the person the link, and say
what it's for. A request lapses after about 10 minutes.

## Notifications

The person is told automatically when you stop to ask them something, need a permission, or sit
idle waiting for them. A hook runs `sws notify send` with a link to this container's console. It
reaches them only once a member has approved this container: if `sws notify grants` lists no one,
offer to run `sws notify request "Claude in <container>"` and give them the link.

Send one yourself (`sws notify send`) only for something they asked to hear about, such as a long
build or run that finished or failed. Don't send one for each step.
