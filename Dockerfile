# Base images pinned by digest: cgr.dev/chainguard/go:latest (go1.27.3) and cgr.dev/chainguard/static:latest, resolved 2026-10-24.
FROM cgr.dev/chainguard/go:latest@sha256:c81d42afe2d9f5728a63998d93340553b46b0a8f149888527f1ab9deb3dfa9ef AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/ericfitz/agentbus2/internal/version.Version=${VERSION}" -o /out/agentbus2 . \
    && mkdir -p /out/data

FROM cgr.dev/chainguard/static:latest@sha256:c12f0107f8afb0bbc1b0eff96ff8675e85a10c6083d039b777c51fef5c27420f
COPY --from=build /out/agentbus2 /agentbus2
# 65532 is Chainguard's nonroot uid and gid; numeric, so this does not depend on the base image's /etc/passwd.
COPY --from=build --chown=65532:65532 /out/data /data
VOLUME ["/data"]
USER nonroot
ENTRYPOINT ["/agentbus2", "server"]
CMD []
