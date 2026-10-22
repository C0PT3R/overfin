# Build a static binary, then ship it alone in a minimal image.
#
# docker build -t overfin .
# The build runs on the builder's own platform and cross-compiles for the
# target, so images for other architectures build without emulation.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X github.com/overfin/server.Version=${VERSION}" -o /overfin ./cmd/overfin
# The final image has no shell, so the data and secrets folders are
# prepared here.
RUN mkdir /data /secrets

# distroless/static: no shell, no package manager, runs as a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /overfin /overfin
# 65532 is the distroless "nonroot" user the container runs as.
COPY --from=build --chown=65532:65532 /data /data
# The secret key Overfin generates on its first start, when none is given:
# a volume of its own, so a copy of the data never carries its key.
COPY --from=build --chown=65532:65532 /secrets /secrets
VOLUME /data /secrets
# 8080: the site (plain http, or Let's Encrypt's checks with https.domain);
# 8443: the site over HTTPS, with https.domain; 8081: the control panel's own
# listener, with an identity provider.
EXPOSE 8080 8443 8081
ENTRYPOINT ["/overfin"]
