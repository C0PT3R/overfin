# Build a static binary, then ship it alone in a minimal image.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /overfin ./cmd/overfin
# The final image has no shell, so the data folder is prepared here.
RUN mkdir /data

# distroless/static: no shell, no package manager, runs as a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /overfin /overfin
# 65532 is the distroless "nonroot" user the container runs as.
COPY --from=build --chown=65532:65532 /data /data
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/overfin"]
