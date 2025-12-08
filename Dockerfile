# Global ARGs shared by all stages
ARG DEBIAN_FRONTEND=noninteractive
ARG GOPATH=/usr/local/go

### first stage - builder ###
FROM debian:trixie-slim AS builder

ARG DEBIAN_FRONTEND
ARG GOPATH
ENV GOPATH=${GOPATH}

# install fakemachine build and unit-test dependencies
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        gcc \
        golang-go && \
    rm -rf /var/lib/apt/lists/*

# Build fakemachine
ARG FAKEMACHINE_VER
COPY . $GOPATH/src/github.com/go-debos/fakemachine
WORKDIR $GOPATH/src/github.com/go-debos/fakemachine/cmd/fakemachine
RUN go install -ldflags="-X main.Version=${FAKEMACHINE_VER}" ./...

### second stage - runner ###
FROM debian:trixie-slim AS runner
RUN apt-get update && \
    apt-get install -y --no-install-recommends initramfs-tools && \
    rm -rf /var/lib/apt/lists/*
RUN rm /etc/kernel/postinst.d/*
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        linux-image-amd64 \
        qemu-system-x86 && \
    rm -rf /var/lib/apt/lists/*

ARG DEBIAN_FRONTEND
ARG GOPATH

# Set HOME to a writable directory in case something wants to cache things
ENV HOME=/tmp

LABEL org.label-schema.name="fakemachine"
LABEL org.label-schema.description="fake a machine"
LABEL org.label-schema.vcs-url="https://github.com/go-debos/fakemachine"
LABEL org.label-schema.docker.cmd='docker run \
  --rm \
  --interactive \
  --tty \
  --device /dev/kvm \
  --user $(id -u) \
  --workdir /work \
  --mount "type=bind,source=$(pwd),destination=/work" \
  --security-opt label=disable'

# fakemachine runtime dependencies
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        binfmt-support \
        busybox \
        qemu-user-static \
        systemd \
        systemd-resolved && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder $GOPATH/bin/fakemachine /usr/local/bin/fakemachine

ENTRYPOINT ["/usr/local/bin/fakemachine"]
