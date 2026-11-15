# syntax=docker/dockerfile:1

# Multi-arch runtime image strategy:
# - Binary cross-compiled offline by CI (dist/<TARGETARCH>/vmt-23)
# - Reduces multi-arch build time: arm64 skips Go/Node compilation, only modifies base layers
# 
# Local build preparation:
#   cd web && npm run build:static && cd ..
#   cp -r web/out server/webui/dist
#   CGO_ENABLED=0 GOARCH=amd64 go build -tags embedui -o dist/amd64/vmt-23 ./cmd/vmt-23
#   docker build -t vmt-23:local .

FROM python:3.12-slim-bookworm AS base
# Setup: install base tools and Node.js from NodeSource (Bookworm has 18.x, need 20+)
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
      ca-certificates \
      ripgrep \
      curl \
      wget \
      vim \
      git \
      jq \
      unzip \
      dnsutils \
      iputils-ping \
      netcat-openbsd \
      inetutils-telnet \
      whois \
      nmap \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

FROM base AS playwright-stage
# Pre-install Playwright globally to avoid repeated downloads in runtime
# Install global tools with cache-busting (use specific versions for reproducibility)
RUN npm install -g --no-save \
      @playwright/mcp@latest \
      @playwright/cli@latest \
      playwright@latest \
    && playwright-cli --help \
    && playwright install --with-deps chromium \
    && npm cache clean --force \
    && rm -rf /var/lib/apt/lists/*

FROM base AS runtime
# Copy pre-installed npm packages from playwright stage
COPY --from=playwright-stage /usr/local/lib/node_modules /usr/local/lib/node_modules
COPY --from=playwright-stage /usr/local/bin /usr/local/bin
COPY --from=playwright-stage /root/.cache /root/.cache

# Non-root user for security (optional, adjust if needed)
# RUN useradd -m -u 1001 vmt-23 && chown -R vmt-23:vmt-23 /app
# USER vmt-23

WORKDIR /app

# Copy pre-compiled binary (CI cross-compiles to dist/<TARGETARCH>/vmt-23)
ARG TARGETARCH
COPY dist/${TARGETARCH}/vmt-23 /app/vmt-23

# Copy runtime scripts
COPY start.sh /app/start.sh
RUN chmod +x /app/vmt-23 /app/start.sh

# Copy skills plugin directory
COPY skills/ /app/skills/

# Healthcheck: monitor main port
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
  CMD curl -f http://localhost:8787/health || exit 1

# Persistent volume for SQLite + JWT key
VOLUME ["/app/data"]

# Expose main UI and proxy ports
EXPOSE 8787 8788

ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]
