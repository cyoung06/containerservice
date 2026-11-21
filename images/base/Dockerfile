# base: Ubuntu 24.04 meeting containerservice's image contract. The login user is dev (uid 1000,
# passwordless sudo); sshd takes keys only, from the console (/etc/containerservice/authorized_keys,
# through AuthorizedKeysCommand) or ~/.ssh/authorized_keys, and uses the container's own host key from /etc/containerservice-host.
FROM docker.io/library/ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive LANG=C.UTF-8 CONTAINERSERVICE_USER=dev
RUN apt-get update && apt-get install -y --no-install-recommends \
      openssh-server openssh-client sudo tini ca-certificates curl wget gnupg \
      git vim nano tmux htop less man-db bash-completion locales tzdata \
      build-essential pkg-config python3 python3-pip python3-venv \
      file unzip zip xz-utils jq ripgrep \
      iproute2 iputils-ping dnsutils netcat-openbsd socat procps psmisc lsof strace \
 && rm -rf /var/lib/apt/lists/* \
 && userdel -r ubuntu \
 && useradd -m -u 1000 -s /bin/bash -G sudo dev \
 && usermod -p '*' dev \
 && echo 'dev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/dev && chmod 0440 /etc/sudoers.d/dev \
 && rm -f /etc/ssh/ssh_host_*
COPY sshd.conf /etc/ssh/sshd_config.d/containerservice.conf
COPY entrypoint /usr/local/bin/containerservice-entrypoint
EXPOSE 22
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/containerservice-entrypoint"]
