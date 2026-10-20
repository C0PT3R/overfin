# Build a static binary, then ship it alone in a minimal image.
#
# The official build: docker build -t overfin .
# With extra modules (each a Go package exporting a Module type, optionally
# @version), added to the official ones:
#   docker build --build-arg MODULES="example.org/mymodule@v1.2.0" -t overfin .
# The build runs on the builder's own platform and cross-compiles for the
# target, so images for other architectures build without emulation.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG MODULES=""
# set -f: module names are never expanded as file patterns.
RUN set -f; target=./cmd/overfin; \
    if [ -n "$MODULES" ]; then go run ./cmd/overfin-build $MODULES && target=./cmd/custom; fi; \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X github.com/C0PT3R/overfin.Version=${VERSION}" -o /overfin "$target"
# The final image has no shell, so the data folder is prepared here.
RUN mkdir /data

# distroless/static: no shell, no package manager, runs as a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /overfin /overfin
# 65532 is the distroless "nonroot" user the container runs as.
COPY --from=build --chown=65532:65532 /data /data
VOLUME /data
EXPOSE 8080 8081
ENTRYPOINT ["/overfin"]
