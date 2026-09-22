# React/TypeScript is compiled during the image build; Node is not shipped.
FROM node:24-bookworm AS frontend
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
COPY web/ /src/web/
RUN npm run build

# Build the control plane, then ship a single static binary.
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS build
ARG TARGETOS TARGETARCH
ARG VERSION=""
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/frontend/dist/assets /src/web/static/react/assets
COPY --from=frontend /src/frontend/dist/index.html /src/web/index.html
COPY --from=frontend /src/frontend/dist/terminal.html /src/web/static/terminal.html
COPY --from=frontend /src/frontend/dist/sw.js /src/web/static/sw.js
# CGO off: modernc.org/sqlite is pure Go, so the result runs on any base image
# Cross-compiled on the builder's own architecture, so a multi-arch image
# does not run the Go toolchain under emulation. The tag is stamped in.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X github.com/JeremiahM37/lectern/v2/internal/version.Version=${VERSION#v}" -o /out/lectern ./cmd/lectern

FROM debian:bookworm-slim
# git and tmux drive the LOCAL target; openssh-client reaches remote ones
RUN apt-get update && apt-get install -y --no-install-recommends \
    git tmux openssh-client curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# ttyd powers one-click terminal attach (optional but recommended)
ARG TARGETARCH
RUN case "$TARGETARCH" in amd64) t=x86_64 ;; arm64) t=aarch64 ;; *) t="" ;; esac; \
    [ -z "$t" ] || { curl -fsSL -o /usr/local/bin/ttyd \
    "https://github.com/tsl0922/ttyd/releases/download/1.7.7/ttyd.$t" \
    && chmod +x /usr/local/bin/ttyd; } || true

COPY --from=build /out/lectern /usr/local/bin/lectern

# NOTE: runs as root deliberately — the container drives targets over SSH with
# keys bind-mounted from the host (see deploy/docker-compose.yml) and manages
# tmux sessions for the `local` target. Harden by scoping the mounted key.
ENV LECTERN_DB=/data/lectern.db
VOLUME /data
EXPOSE 9110

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s \
  CMD curl -fsS "http://127.0.0.1:${LECTERN_PORT:-9110}/api/health" || exit 1

CMD ["lectern"]
