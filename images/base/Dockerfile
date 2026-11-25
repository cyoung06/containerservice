# base: Ubuntu 24.04 meeting containerservice's image contract. The login user is dev (uid 1000,
# passwordless sudo); sshd takes keys only, from the console (/etc/containerservice/authorized_keys,
# through AuthorizedKeysCommand) or ~/.ssh/authorized_keys, and uses the container's own host key from /etc/containerservice-host.
FROM docker.io/library/ubuntu:24.04
# rclone from upstream: Ubuntu's 1.60 mounts only through FUSE 2's fusermount, which isn't here.
# fusermount3 (the setuid helper unprivileged FUSE mounts go through) lives at /usr/local/bin: on an
# Ubuntu node, container processes are stacked with "unconfined", whose half follows the host's
# profiles by path, and the host's fusermount3 profile (for /usr/bin/fusermount3) breaks it in a
# container. Elsewhere only containerservice's profile applies. libfuse and rclone find it on PATH.
ARG RCLONE=1.76.0
ENV DEBIAN_FRONTEND=noninteractive LANG=C.UTF-8 CONTAINERSERVICE_USER=dev
RUN apt-get update && apt-get install -y --no-install-recommends \
      openssh-server openssh-client sudo tini ca-certificates curl wget gnupg \
      git vim nano tmux htop less man-db bash-completion locales tzdata \
      build-essential pkg-config python3 python3-pip python3-venv \
      file unzip zip xz-utils jq ripgrep \
      fuse3 sshfs \
      iproute2 iputils-ping dnsutils netcat-openbsd socat procps psmisc lsof strace \
 && rm -rf /var/lib/apt/lists/* \
 && userdel -r ubuntu \
 && useradd -m -u 1000 -s /bin/bash -G sudo dev \
 && usermod -p '*' dev \
 && echo 'dev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/dev && chmod 0440 /etc/sudoers.d/dev \
 && rm -f /etc/ssh/ssh_host_* \
 && curl -fsSLo /tmp/rclone.deb https://github.com/rclone/rclone/releases/download/v${RCLONE}/rclone-v${RCLONE}-linux-amd64.deb \
 && dpkg -i /tmp/rclone.deb && rm /tmp/rclone.deb \
 && ln -sf /proc/self/mounts /etc/mtab \
 && dpkg-divert --local --rename --divert /usr/local/bin/fusermount3 --add /usr/bin/fusermount3
COPY sshd.conf /etc/ssh/sshd_config.d/containerservice.conf
COPY entrypoint /usr/local/bin/containerservice-entrypoint
# A token of the container's own service account for any audience (drive, forge, ...); with "FUSE
# mounts" on, sshfs and rclone mount (fuse3).
COPY sa-token /usr/local/bin/sa-token
EXPOSE 22
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/containerservice-entrypoint"]
