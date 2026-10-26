# syntax=docker/dockerfile:1

# Build the service from this repository's source. The runtime stage starts
# from a fresh image so compiler tools and source files cannot become part of
# the public image.
FROM golang:1.24-bookworm AS builder

WORKDIR /src
ARG TARGETARCH

# Copy the whole context instead of a hand-maintained list of directories. The
# previous allowlist silently omitted new packages (internal/plugin,
# internal/storage, internal/notify, plugins/github) and every CI run failed at
# go build while local tests passed. What may enter the context is decided in
# .dockerignore, and cmd/folio's guard test keeps that allowlist honest.
COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags='-s -w' -o /out/folio ./cmd/folio

FROM node:24-bookworm-slim AS runtime

ARG TARGETARCH
ARG GH_VERSION=2.102.0

ENV FOLIO_CONTEXT_ROOT=/data/repository \
    FOLIO_RUNTIME=/data/runtime \
    SERVICE_DATA_DIR=/data \
    DSH_HOME=/data/dsh \
    HOME=/home/context

RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \
    set -eux; \
    apt-get update; \
    apt-get install --no-install-recommends -y \
        ca-certificates \
        curl \
        git \
        gosu; \
    gh_targetarch="${TARGETARCH:-amd64}"; \
    case "${gh_targetarch}" in \
        amd64) gh_sha256=bb766f710eef8ede859c18578c72c327597cd4c8a85b06001b1f3843c6019386 ;; \
        arm64) gh_sha256=7862c86c72f43df3a2d93ddde6f473285b4e2af61b494849846827e513ef6484 ;; \
        *) echo "unsupported TARGETARCH: ${gh_targetarch}" >&2; exit 1 ;; \
    esac; \
    gh_archive="gh_${GH_VERSION}_linux_${gh_targetarch}.tar.gz"; \
    curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
        --connect-timeout 20 --max-time 180 --retry 3 --retry-delay 2 --retry-all-errors \
        --output "/tmp/${gh_archive}" \
        "https://github.com/cli/cli/releases/download/v${GH_VERSION}/${gh_archive}"; \
    printf '%s  %s\n' "${gh_sha256}" "/tmp/${gh_archive}" | sha256sum -c -; \
    tar -xzf "/tmp/${gh_archive}" -C /tmp; \
    install -m 0755 "/tmp/gh_${GH_VERSION}_linux_${gh_targetarch}/bin/gh" /usr/local/bin/gh; \
    test "$(gh --version | sed -n '1s/^gh version \([^ ]*\).*/\1/p')" = "${GH_VERSION}"; \
    gh auth login --help | grep -F -- '--insecure-storage'; \
    rm -rf "/tmp/${gh_archive}" "/tmp/gh_${GH_VERSION}_linux_${gh_targetarch}" \
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
COPY --chown=context:context internal/channel/weixin/NOTICE /app/WEIXIN-NOTICE
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
