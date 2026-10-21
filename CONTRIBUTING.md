# Contributing to hyperbolic-go

Bug reports, fixes and features are welcome. For a new feature or a change to the public Go API, open an issue first so we can agree on the design. Report security issues privately, as [SECURITY.md](SECURITY.md) describes.

## Development

`make help` lists every target. The common ones:

```console
make test   # unit tests with the race detector
make cover  # coverage summary
make lint   # golangci-lint
make vuln   # govulncheck
```

Tool versions are pinned. `make lint` installs golangci-lint into `./bin`.

Every Go file starts with the license header in `hack/boilerplate.go.txt`. The `goheader` linter checks it.

Tests never call the real Hyperbolic API. Serve the responses a test needs from an `httptest.Server`.

## Pull requests

- One logical change per pull request.
- Every behavior change comes with tests. Code that creates or terminates rentals needs tests for its failure modes.
- Title the pull request `type(scope): summary`, following [Conventional Commits](https://www.conventionalcommits.org/), and write it for the release notes. Pull requests are squash-merged, so the title is also the commit on `main`.
- A breaking change to the Go API needs `!` in the title and a description of what users must change.
- CI must pass before review.

## Releases

Releases follow [semantic versioning](https://semver.org/). Before 1.0 a minor version may break the Go API, and the release notes call out every breaking change.
