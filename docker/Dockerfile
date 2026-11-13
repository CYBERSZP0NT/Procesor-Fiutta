# syntax=docker/dockerfile:1
# szmelc-router — single static binary, distroless runtime, non-root.
#
# Build (from the repo root):
#   docker build -t szmelc-router:latest -f docker/Dockerfile .
#
# Or simply:
#   docker compose -f docker/docker-compose.yml up --build
#
# The image contains only the statically-linked `szmelc-router` binary and
# the example plugins (used by docker-compose.zerokey.yml). Config is
# mounted at /config (or pointed to with FREELLM_CONFIG); usage journal is
# written under /data (mount a volume there to persist it).

FROM golang:1.27 AS build
WORKDIR /src
# Cache deps.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=1970-01-01T00:00:00Z
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
      -o /out/bin/szmelc-router ./cmd/szmelc-router \
 && ln -s szmelc-router /out/bin/freellm-router  # legacy name, pre-rebrand

# Runtime: distroless/static = no shell, no libc, minimal attack surface.
FROM gcr.io/distroless/static:nonroot AS runtime
# Copying the directory keeps the freellm-router symlink a symlink.
COPY --from=build /out/bin/ /usr/local/bin/
# Example plugins (optional; the zero-key compose uses the python one).
COPY plugins /plugins
ENV HOME=/data \
    XDG_CONFIG_HOME=/config \
    XDG_DATA_HOME=/data \
    FREELLM_CONFIG=/config/config.yaml
USER nonroot:nonroot
EXPOSE 8787
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD [ "/usr/local/bin/szmelc-router", "config", "validate", "--config", "/config/config.yaml" ]
ENTRYPOINT [ "szmelc-router" ]
CMD [ "serve", "--host", "0.0.0.0", "--port", "8787" ]
