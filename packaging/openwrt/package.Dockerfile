ARG SDK=ghcr.io/openwrt/sdk:aarch64_generic-24.10.8
FROM ${SDK} AS package
USER root
WORKDIR /builder
COPY . /tmp/input/
ARG RELEASE
ARG ARCH
ARG VERSION
ARG REVISION
ARG PACKAGE_ARCH
ARG FEED_MODE
ARG PRERELEASE
RUN sh /tmp/input/package.sh

FROM scratch
COPY --from=package /tmp/feed/ /
