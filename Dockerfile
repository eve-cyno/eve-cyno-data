# syntax=docker/dockerfile:1.7
#
# eve-cyno-data: the data API (cmd/dataapi): OpenAPI, /llms.txt, the keyed tool endpoint and
# the MCP endpoint over the deterministic EVE tools. Also ships the SDE builder (cmd/sde) as a
# second binary, so a self-hoster can build the SDE into a volume with the same image.
# Pure-Go SQLite (modernc.org/sqlite) -> CGO_ENABLED=0, so the runtime stage needs no libc / shell.
# Build context = the repository root.

# -- Build stage ---------------------------------------------------------------------------
FROM golang:1.27.2-trixie AS build
WORKDIR /src

# Cache module downloads on go.mod / go.sum only (the common edit case is code).
COPY go.mod go.sum ./
RUN go mod download

COPY . ./
# VERSION is the release tag (release.yml passes it as a build arg). It is stamped into the
# binaries through version.buildVersion; API_VERSION, when set, still takes precedence at runtime.
# Empty (a plain `docker build`) leaves the binary on its runtime/fallback resolution.
ARG VERSION=
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X eve-cyno.dev/go/data/version.buildVersion=${VERSION}" \
      -o /out/dataapi ./cmd/dataapi && \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X eve-cyno.dev/go/data/version.buildVersion=${VERSION}" \
      -o /out/sde ./cmd/sde

# -- Runtime stage (distroless static: no shell, no libc; CA bundle for the SDE download) --
FROM gcr.io/distroless/static-debian13:nonroot AS runtime
COPY --from=build /out/dataapi /dataapi
COPY --from=build /out/sde /sde

# The SDE is supplied at runtime through a volume mounted at /data/sde (read-only is enough to
# serve). Build it with the second binary:
#   docker run --rm --user 0:0 -v sde_data:/data/sde --entrypoint /sde <image>
# (root only because a fresh named volume is root-owned; the file it writes is 0644).
# EVE_CORE_SDE_PATH MUST point at the mount: the source-relative default does not exist in the
# container.
ENV EVE_CORE_SDE_PATH=/data/sde/sde.sqlite \
    DATAAPI_ADDR=:8092

LABEL org.opencontainers.image.source="https://github.com/eve-cyno/eve-cyno-data" \
      org.opencontainers.image.title="eve-cyno-data" \
      org.opencontainers.image.licenses="MIT"

EXPOSE 8092

# No HEALTHCHECK: distroless has no curl/wget. dataapi serves GET /v1/health; use an
# orchestrator-level TCP/HTTP check.
ENTRYPOINT ["/dataapi"]
