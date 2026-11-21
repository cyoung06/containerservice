# claude: base plus Claude Code and what it's usually asked to drive: Node, gh, Go, uv. Claude
# skips permissions by default; containers are hardened unless made with Debugging.
ARG BASE=oci.syeyoung.dev/containers/base:latest
FROM ${BASE}
ARG GO=1.27.4
ARG NODE_MAJOR=24
RUN curl -fsSL https://deb.nodesource.com/setup_${NODE_MAJOR}.x | bash - \
 && curl -fsSLo /etc/apt/keyrings/githubcli.gpg https://cli.github.com/packages/githubcli-archive-keyring.gpg \
 && echo "deb [signed-by=/etc/apt/keyrings/githubcli.gpg] https://cli.github.com/packages stable main" > /etc/apt/sources.list.d/github-cli.list \
 && apt-get update && apt-get install -y --no-install-recommends nodejs gh fd-find \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g @anthropic-ai/claude-code \
 && npm cache clean --force \
 && curl -fsSL https://go.dev/dl/go${GO}.linux-amd64.tar.gz | tar -C /usr/local -xz \
 && curl -LsSf https://astral.sh/uv/install.sh | env UV_INSTALL_DIR=/usr/local/bin UV_NO_MODIFY_PATH=1 sh \
 && printf '%s\n' 'export PATH=/usr/local/go/bin:$HOME/go/bin:$HOME/.local/bin:$PATH' 'export DISABLE_AUTOUPDATER=1' > /etc/profile.d/claude.sh
ENV PATH=/usr/local/go/bin:$PATH DISABLE_AUTOUPDATER=1
COPY sshd.conf /etc/ssh/sshd_config.d/00-claude.conf
# Skipping permissions is the default here (managed settings; the console's shared settings are a
# drop-in in managed-settings.d, merged over these); the console's token reaches login shells.
COPY managed-settings.json /etc/claude-code/managed-settings.json
COPY claude-token.sh /etc/profile.d/claude-token.sh
