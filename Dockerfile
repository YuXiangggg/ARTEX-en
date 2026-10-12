# syntax=docker/dockerfile:1
#
# Multi-stage build: static frontend export -> Go single binary (embedded frontend) -> runtime image.
# The original Docker Hub image (autumn27/artex) is gone; this repo now supports self-building from pure source:
#   docker compose build        # or docker build -t artex:local .
# TARGETARCH is injected automatically by buildx (amd64/arm64); Go cross-compiles without QEMU;
# the runtime layer only installs tools, all compilation happens in the build stages.
# On networks in mainland China you can override the module sources (defaults are already China-friendly):
#   --build-arg GOPROXY=https://goproxy.cn,direct
#   --build-arg NPM_REGISTRY=https://registry.npmmirror.com

########## Stage 1: static frontend export ##########
FROM node:20-bookworm AS web
ARG NPM_REGISTRY=https://registry.npmmirror.com
WORKDIR /web
# --ignore-scripts: skip prepare (husky) -- there is no .git in the container, so husky would fail;
# Next.js/Biome platform binaries come via optionalDependencies and don't rely on lifecycle scripts.
COPY web/package.json web/package-lock.json ./
RUN npm config set registry "$NPM_REGISTRY" && npm ci --ignore-scripts
COPY web/ ./
RUN NEXT_EXPORT=1 npm run build:static

########## Stage 2: Go backend (embedded frontend, static build) ##########
FROM golang:1.26-bookworm AS gobuild
ARG TARGETARCH
ARG GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Copy static frontend output from the web stage (.dockerignore already excludes the host dist, so no conflict)
COPY --from=web /web/out/ server/webui/dist/
RUN CGO_ENABLED=0 GOARCH="${TARGETARCH}" go build -tags embedui -trimpath -o /out/artex ./cmd/artex

########## Stage 3: runtime image (same as the original: tools + preinstalled Playwright) ##########
FROM python:3.12-slim-bookworm
ARG NPM_REGISTRY=https://registry.npmmirror.com
# Global npm installs and the chromium download in the runtime layer also use China mirrors (reachable from global networks too)
ENV npm_config_registry=$NPM_REGISTRY \
    PLAYWRIGHT_DOWNLOAD_HOST=https://cdn.npmmirror.com/binaries/playwright
# Common tools: ripgrep / curl / vim, plus a set of recon staples (add/remove as needed).
# Install Node 20.x from NodeSource: bookworm's apt nodejs is 18, and Playwright requires >=20.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates ripgrep curl wget vim git jq unzip \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
# Preinstall Playwright MCP and CLI (globally) so nothing is downloaded via npx at runtime.
# @playwright/mcp: the browser MCP runs `npx @playwright/mcp` directly (installed globally, no -y/@latest needed).
# @playwright/cli: provides playwright-cli; after install, run --help to verify it is executable.
# Then install playwright (browser management) and use --with-deps to preinstall chromium and its system dependencies,
# so MCP/CLI work on first start inside the container without downloading a browser.
RUN npm install -g @playwright/mcp@latest @playwright/cli@latest playwright@latest \
    && playwright-cli --help \
    && playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
# Single binary produced by the build stage (embedded frontend)
COPY --from=gobuild /out/artex /app/artex
# Supervisor launch script: after the process exits it decides by exit code whether to restart; the in-page one-click update relies on it to swap binaries.
# It also forwards SIGTERM to artex -- docker stop only signals PID 1;
# without forwarding, artex never gets it, can't shut down gracefully, and is SIGKILLed after 10 seconds.
COPY start.sh /app/start.sh
RUN chmod +x /app/artex /app/start.sh
COPY skills/ /app/skills/
# data/ (SQLite + jwt.key) persistence point
VOLUME ["/app/data"]
EXPOSE 8787 8788
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]
