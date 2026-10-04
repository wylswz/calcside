# syntax=docker/dockerfile:1

# ---- Stage 1: build the web console (web/dist is embedded into the Go binary) ----
FROM node:22-alpine AS web
ARG PNPM_VERSION=10.33.0
RUN npm install -g "pnpm@${PNPM_VERSION}"
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# ---- Stage 2: build the Go binaries (pure-Go sqlite, no CGO) ----
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# Real console assets overwrite the committed web/dist placeholder.
COPY --from=web /src/web/dist ./web/dist
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/calcside ./cmd/calcside && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/csctl ./cmd/csctl && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/calcside-worker ./cmd/calcside-worker

# ---- Stage 3: runtime ----
# git is required at runtime by the ext capability's remote-source fetches;
# su-exec lets the worker entrypoint drop root after cgroup setup.
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata git su-exec && \
    adduser -D -u 10001 -h /home/calcside calcside

COPY --from=build /out/calcside /usr/local/bin/calcside
COPY --from=build /out/csctl /usr/local/bin/csctl
COPY --from=build /out/calcside-worker /usr/local/bin/calcside-worker
COPY --chmod=0755 docker/worker-entrypoint.sh /usr/local/bin/worker-entrypoint

# Everything mutable lives under /data: sqlite db, global rego policies,
# local extension roots, and the remote-ext fetch cache. Empty dirs are
# fine — policies/ext are opt-in and a missing dir just means "none".
RUN mkdir -p /data/policies /data/ext /data/ext-cache && \
    chown -R calcside:calcside /data
USER calcside
VOLUME /data
EXPOSE 8080

# All flags also read from CALCSIDE_* env vars (see internal/config).
ENV CALCSIDE_ADDR=":8080" \
    CALCSIDE_DSN="/data/calcside.db" \
    CALCSIDE_POLICY_DIR="/data/policies" \
    CALCSIDE_EXT_LOCAL_ROOTS="/data/ext" \
    CALCSIDE_EXT_CACHE_DIR="/data/ext-cache" \
    CALCSIDE_BASE_URL="http://localhost:8080"

ENTRYPOINT ["calcside"]
CMD ["serve"]
