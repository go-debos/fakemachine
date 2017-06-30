FROM debian:stretch-slim

ARG DEBIAN_FRONTEND=noninteractive

ENV HOME=/scratch

# Always install procps in case the docker file gets used in jenkins
RUN apt update && apt-get install  --no-install-recommends -y procps

# Bits needed to run fakemachine
RUN apt-get update  && \
    apt-get install --no-install-recommends -y qemu-system-x86 \
                                               qemu-user-static \
                                               busybox \
                                               linux-image-amd64 \
                                               systemd \
                                               dbus

# Bits needed to build fakemachine
RUN apt-get update  && \
    apt-get install --no-install-recommends -y golang-go git ca-certificates

WORKDIR /scratch
