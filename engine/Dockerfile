# Static binary, scratch-adjacent runtime. The engine shells out to git, gh
# and docker, so those have to be present -- but nothing else does, and
# everything the engine runs against a target repository runs in a sibling
# container rather than here.

FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY engine/go.mod engine/go.sum ./
RUN go mod download
COPY engine/ ./
# CGO off so the result does not need a libc at runtime, and -trimpath so the
# build directory does not end up in a binary that may be shared.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/engine ./cmd/pipeline

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates git curl gnupg \
    # gh, for the GitHub API. The CLI rather than a Go SDK because it handles
    # auth, retries and GraphQL, and because every flag stays greppable in
    # the audit log.
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
         -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] \
         https://cli.github.com/packages stable main" > /etc/apt/sources.list.d/github-cli.list \
    # docker CLI only -- no daemon. The engine asks the host's daemon to run
    # sibling containers through the mounted socket.
    && curl -fsSL https://download.docker.com/linux/debian/gpg \
         -o /usr/share/keyrings/docker.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/docker.gpg] \
         https://download.docker.com/linux/debian bookworm stable" > /etc/apt/sources.list.d/docker.list \
    && apt-get update && apt-get install -y --no-install-recommends gh docker-ce-cli \
    && rm -rf /var/lib/apt/lists/*

# Not root. The clone volume is chowned to this uid by the compose setup, and
# a container that builds other containers is quite enough privilege already.
RUN useradd --uid 1000 --create-home --shell /bin/bash ossp \
    && usermod -aG docker ossp 2>/dev/null || true

WORKDIR /app
COPY --from=build /out/engine /usr/local/bin/engine
# Read-only at runtime; the authoritative copy of the caps lives in Postgres.
COPY config/ /app/config/
COPY githooks/ /app/githooks/
COPY bin/gh-token-helper /app/bin/gh-token-helper

ENV OSSP_ROOT=/app
USER ossp
ENTRYPOINT ["engine"]
CMD ["daily"]
