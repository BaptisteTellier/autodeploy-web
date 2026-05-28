# syntax=docker/dockerfile:1.7

############################
# Stage 1 — Build Go binary
############################
FROM golang:1.22-alpine AS gobuild
ARG VERSION=dev
WORKDIR /src
RUN apk add --no-cache git ca-certificates curl
COPY go.mod go.sum* ./
COPY . .
# Fetch vendored JS/CSS into internal/server/static before embed.
RUN sh scripts/fetch-vendor.sh
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
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
FROM mcr.microsoft.com/powershell:7.4-debian-bullseye-slim

ARG AUTODEPLOY_VERSION=dev
LABEL org.opencontainers.image.title="autodeploy-web"
LABEL org.opencontainers.image.description="Web UI + container wrapper around BaptisteTellier/autodeploy PowerShell tool"
LABEL org.opencontainers.image.source="https://github.com/BaptisteTellier/autodeploy-web"
LABEL org.opencontainers.image.licenses="MIT"
LABEL autodeploy.version="${AUTODEPLOY_VERSION}"

RUN apt-get update && apt-get install -y --no-install-recommends \
        xorriso \
        rsync \
        ca-certificates \
        tini \
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
RUN mkdir -p /data/iso /data/output /data/license /data/conf /data/configs \
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
