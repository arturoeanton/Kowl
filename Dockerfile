# Build and run Kowl in a container.
#
#   docker build -t kowl .
#   docker run --rm -v "$PWD":/watch kowl -f /watch -r -x .git -j /watch/example.js
#
# Exclude .git, or a recursive watch spends most of its budget on paths nobody wants a
# hook for: on this repository that is 93 watched paths instead of 4.
#
# Whether filesystem events cross a bind mount from a macOS or Windows host depends on
# how Docker shares files: they do with virtiofs, and historically did not with gRPC-FUSE.
# If a change on the host never reaches a hook, that is why, and polling is the way
# around it: -m 2s notices the change without needing an event.
FROM golang:1.26 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=""
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/kowl .

# A script can call anything through kExec, so the runtime image carries a shell and the
# certificates kCli needs. Distroless would be smaller and much less useful.
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/kowl /usr/local/bin/kowl

ENTRYPOINT ["kowl"]
CMD ["--help"]
