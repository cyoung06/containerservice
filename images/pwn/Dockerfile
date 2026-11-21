# pwn: base plus debuggers and exploit tooling. pwntools and ROPgadget live in /opt/pwn (a venv
# first on PATH, so its python3 imports pwn); pwndbg is the pwndbg command.
ARG BASE=oci.syeyoung.dev/containers/base:latest
FROM ${BASE}
ARG PWNDBG=2026.10.28
ARG RADARE2=6.2.6
RUN apt-get update && apt-get install -y --no-install-recommends \
      gdb gdb-multiarch gdbserver qemu-user ltrace patchelf elfutils binutils nasm \
      gcc-multilib g++-multilib libc6-dbg python3-dev libssl-dev libffi-dev ruby ruby-dev \
 && curl -fsSLo /tmp/pwndbg.deb https://github.com/pwndbg/pwndbg/releases/download/${PWNDBG}/pwndbg_${PWNDBG}_amd64.deb \
 && curl -fsSLo /tmp/radare2.deb https://github.com/radareorg/radare2/releases/download/${RADARE2}/radare2_${RADARE2}_amd64.deb \
 && apt-get install -y /tmp/pwndbg.deb /tmp/radare2.deb \
 && rm -rf /tmp/*.deb /var/lib/apt/lists/* \
 && python3 -m venv /opt/pwn \
 && /opt/pwn/bin/pip install --no-cache-dir pwntools ropgadget \
 && gem install --no-document one_gadget seccomp-tools \
 && echo 'export PATH=/opt/pwn/bin:$PATH' > /etc/profile.d/pwn.sh
ENV PATH=/opt/pwn/bin:$PATH
