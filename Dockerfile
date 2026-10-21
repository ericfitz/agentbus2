# Base images pinned by digest: golang:1.27-alpine and gcr.io/distroless/static-debian12:nonroot, resolved 2026-10-21.
FROM golang:1.27-alpine@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/ericfitz/agentbus2/internal/version.Version=${VERSION}" -o /out/agentbus2 . \
    && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/agentbus2 /agentbus2
# 65532 is distroless's nonroot uid and gid; numeric, so this does not depend on the base image's /etc/passwd.
COPY --from=build --chown=65532:65532 /out/data /data
VOLUME ["/data"]
USER nonroot
ENTRYPOINT ["/agentbus2", "server"]
CMD []
