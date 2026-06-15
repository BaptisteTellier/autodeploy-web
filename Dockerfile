# syntax=docker/dockerfile:1.7

############################
# Stage 1 — Build Go binary
############################
FROM golang:1.25-alpine AS gobuild
ARG VERSION=dev
ARG COMMIT=
ARG BUILD_DATE=
WORKDIR /src
RUN apk add --no-cache git ca-certificates curl
COPY go.mod go.sum* ./
COPY . .
# Fetch vendored JS/CSS into internal/server/static before embed.
RUN sh scripts/fetch-vendor.sh
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE}" \
    -o /out/autodeploy-web ./cmd/autodeploy-web

############################
# Stage 2 — Fetch autodeploy.ps1
############################
FROM alpine:3.20 AS fetch
ARG AUTODEPLOY_VERSION=dev
ARG AUTODEPLOY_REPO=https://github.com/BaptisteTellier/autodeploy.git
RUN apk add --no-cache git
RUN git clone --depth 1 --branch ${AUTODEPLOY_VERSION} ${AUTODEPLOY_REPO} /autodeploy
RUN echo "${AUTODEPLOY_VERSION}" > /autodeploy/.pinned-version

############################
# Stage 3 — Runtime
############################
# Debian base + PowerShell from the official GitHub release tarball. We do NOT
# use mcr.microsoft.com/powershell: MCR's anonymous-pull token endpoint
# rate-limits CI runners (HTTP 401 / 429) and intermittently broke image builds.
# GitHub + Docker Hub (used by the other stages) are the reliable registries here.
FROM debian:bookworm-slim

ARG AUTODEPLOY_VERSION=dev
ARG PWSH_VERSION=7.4.6
LABEL org.opencontainers.image.title="autodeploy-web"
LABEL org.opencontainers.image.description="Web UI + container wrapper around BaptisteTellier/autodeploy PowerShell tool"
LABEL org.opencontainers.image.source="https://github.com/BaptisteTellier/autodeploy-web"
LABEL org.opencontainers.image.licenses="MIT"
LABEL autodeploy.version="${AUTODEPLOY_VERSION}"

# App runtime deps (xorriso/rsync are invoked by autodeploy.ps1) + the shared
# libraries PowerShell 7.4 needs on Debian 12, then pwsh itself (linux-x64).
RUN apt-get update && apt-get install -y --no-install-recommends \
        xorriso \
        rsync \
        ca-certificates \
        curl \
        tini \
        less \
        libicu72 \
        libssl3 \
    && curl -fsSL "https://github.com/PowerShell/PowerShell/releases/download/v${PWSH_VERSION}/powershell-${PWSH_VERSION}-linux-x64.tar.gz" -o /tmp/pwsh.tar.gz \
    && mkdir -p /opt/microsoft/powershell/7 \
    && tar -xzf /tmp/pwsh.tar.gz -C /opt/microsoft/powershell/7 \
    && chmod +x /opt/microsoft/powershell/7/pwsh \
    && ln -sf /opt/microsoft/powershell/7/pwsh /usr/bin/pwsh \
    && rm -f /tmp/pwsh.tar.gz \
    && rm -rf /var/lib/apt/lists/*

# Wrapper "wsl" — the PS1 calls "wsl xorriso ..." on Windows.
# Inside the container we forward to the native binary.
COPY scripts/wsl-wrapper.sh /usr/local/bin/wsl
RUN chmod +x /usr/local/bin/wsl

# Wrapper "cmd" — the PS1 uses "cmd /c <command>" to capture output on Windows.
# Inside the container we re-dispatch to bash.
COPY scripts/cmd-wrapper.sh /usr/local/bin/cmd
RUN chmod +x /usr/local/bin/cmd

# Application binary
COPY --from=gobuild /out/autodeploy-web /usr/local/bin/autodeploy-web

# autodeploy.ps1 (pinned version)
COPY --from=fetch /autodeploy /opt/autodeploy

# Data directories (mounted as volumes in production)
RUN mkdir -p /data/iso /data/output /data/license /data/conf /data/configs /data/work \
    && chmod -R 0777 /data

ENV LISTEN_ADDR=":8080" \
    DATA_DIR="/data" \
    AUTODEPLOY_DIR="/opt/autodeploy" \
    PS_SCRIPT="autodeploy.ps1" \
    WORKER_CONCURRENCY="1"

EXPOSE 8080
WORKDIR /data/iso

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/usr/local/bin/autodeploy-web"]
