# Build a static binary, then ship it alone in a minimal image.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /overfin ./cmd/overfin

# distroless/static: no shell, no package manager, runs as a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /overfin /overfin
EXPOSE 8080
ENTRYPOINT ["/overfin"]
