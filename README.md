# Overfin

A secure gateway between a Jellyfin server and a website you build yourself.

Overfin sits next to Jellyfin and exposes a small, fixed HTTP API under `/api/v1/`.
Your website (any stack, even static HTML) calls that API on its own domain.
The browser never talks to Jellyfin, never sees a Jellyfin token, and can't
reach anything Overfin doesn't explicitly offer. Seerr support (search and
requests) is an optional plugin.

```
browser ──▶ your website
   └──▶ /api/v1/*  ──▶  Overfin  ──▶  Jellyfin (+ Seerr)
```

> Status: early development. Nothing here is ready to run in production yet.

## Build and run

```sh
go test ./...
go run ./cmd/overfin               # listens on :8080
curl localhost:8080/api/v1/health  # {"status":"ok"}
```

Docker:

```sh
docker build -t overfin .
docker run --rm -p 8080:8080 overfin
```

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `OVERFIN_LISTEN` | `:8080` | Address the gateway listens on |

## Security

See [SECURITY.md](SECURITY.md) for the rules the gateway enforces and how to
report a vulnerability.

## AI-assisted development

Overfin is developed with the help of AI. Much of the code and documentation
is written together with Claude (Anthropic), and commits it contributed to
carry a `Co-Authored-By: Claude` trailer. Every change is reviewed by a human
maintainer, and the security-critical paths (login, sessions, stream
authorization) are read line by line.

## License

[MIT](LICENSE)
