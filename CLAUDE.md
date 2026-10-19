# Overfin

Go gateway between Jellyfin (required), optional TMDB and Seerr plugins, and a
website someone else builds. MIT, public.

## Non-negotiable
- Follow every rule in SECURITY.md. Never add a passthrough or catch-all route;
  routes go through `plugin.Registrar`, which enforces this.
- Never copy or port code from other projects (Streamyfin, jellyfin-web, etc.).
  Build from the Jellyfin, Seerr and TMDB API specs and official docs.
- Standard library first. Any new dependency needs a stated reason.
- This repo is public. Never commit details of the maintainer's own setup:
  domains, IPs, hostnames, usernames, emails, network layout or secrets. Use
  `example.org` and documentation IPs (192.0.2.0/24, 198.51.100.0/24) in
  examples.
- AI assistance is always disclosed: keep the README's "AI-assisted
  development" section accurate, and end commits Claude contributed to with a
  `Co-Authored-By: Claude ...` trailer.

## Style
- Plain, idiomatic Go. Clear code over clever code: code that is easy to read
  is code that can be reviewed, and review is part of security.
- `gofmt`, `go vet ./...` and `go test ./...` must pass, and
  `node --test sdk/js/overfin.test.js` for the JavaScript SDK.
- `examples/demo-site/overfin.js` is a copy of `sdk/js/overfin.js`; a test
  keeps them identical, so copy it over after changing the SDK.

## Layout
- `cmd/overfin`: entry point
- `internal/server`: HTTP handler, middleware
- `internal/plugin`: plugin interface and the route registrar
