# syntax=docker/dockerfile:1

# Build the service from this repository's source. The runtime stage starts
# from a fresh image so compiler tools and source files cannot become part of
# the public image.
FROM golang:1.24-bookworm AS builder

WORKDIR /src
ARG TARGETARCH
COPY go.mod ./go.mod
COPY cmd/folio/main.go ./cmd/folio/main.go
COPY internal/context/ ./internal/context/
COPY internal/store/ ./internal/store/
COPY internal/web/ ./internal/web/

RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags='-s -w' -o /out/folio ./cmd/folio

FROM node:24-bookworm-slim AS runtime

ENV FOLIO_CONTEXT_ROOT=/data/repository \
    FOLIO_RUNTIME=/data/runtime \
    SERVICE_DATA_DIR=/data \
    DSH_HOME=/data/dsh \
    HOME=/home/context

RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \
    apt-get update \
    && apt-get install --no-install-recommends -y \
        ca-certificates \
        git \
        gh \
        gosu \
    && rm -rf /var/lib/apt/lists/*

# Keep the CLI version exact. A floating npm tag would make image contents
# change without a source change.
RUN npm install --global --omit=dev --no-audit --no-fund @openai/codex@0.147.0 \
    && npm cache clean --force \
    && test "$(codex --version | awk '{print $NF}')" = "0.147.0"

# The DSH headless runner uses the same pinned installation style. Its provider
# credential file is created at runtime in the private /data volume, never here.
RUN npm install --global --omit=dev --no-audit --no-fund @deepseek-ai/dsh@0.2.1-rc.2 \
    && npm cache clean --force \
    && test "$(dsh --version | awk '{print $NF}')" = "0.2.1-rc.2"

RUN groupadd --system --gid 10001 context \
    && useradd --system --uid 10001 --gid context --create-home --home-dir /home/context context \
    && mkdir -p /app/web /data \
    && chown -R context:context /app /data /home/context

WORKDIR /app

COPY --from=builder --chown=context:context /out/folio /app/folio
COPY deploy/entrypoint /usr/local/bin/folio-entrypoint
COPY --chown=context:context web/ /app/web/

RUN chmod 0555 /app/folio /usr/local/bin/folio-entrypoint \
    && find /app/web -type f -exec chmod 0444 {} + \
    && mkdir -p /data/runtime /data/home /data/dsh \
    && chmod 0700 /data/dsh \
    && chown -R context:context /data

VOLUME ["/data"]
EXPOSE 8080
USER root
ENTRYPOINT ["/usr/local/bin/folio-entrypoint"]
CMD ["/app/folio", "serve", "--host", "0.0.0.0", "--port", "8080"]
