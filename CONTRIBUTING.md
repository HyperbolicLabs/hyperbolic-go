# Contributing to hyperbolic-go

Bug reports, fixes and features are welcome. For a new feature or a change to the public Go API, open an issue first so we can agree on the design. Report security issues privately, as [SECURITY.md](SECURITY.md) describes.

## Development

`make help` lists every target. The common ones:

```console
make test              # unit tests with the race detector
make cover             # coverage summary
make lint              # golangci-lint on the SDK and the generator
make vuln              # govulncheck
make generate          # regenerate the SDK from openapi/
make verify-generated  # fail if the generated files are stale
make spec-sync         # pull the published spec and regenerate
```

Tool versions are pinned. `make lint` installs golangci-lint into `./bin`.

Every Go file starts with the license header in `hack/boilerplate.go.txt`. The `goheader` linter checks it and the generator writes it.

Tests never call the real Hyperbolic API. Serve the responses a test needs from an `httptest.Server`.

## Generated code

Files ending in `_gen.go` or `_gen_test.go`, and `testdata/responses/`, are written by `make generate`. Never edit them by hand. Change their source instead.

- The API's shape comes from the published spec, or from `openapi/overlay.yaml` until a fix is published.
- Names, retries and pagination come from `openapi/sdk.yaml`.
- The code itself comes from `internal/gen/templates/`.

[openapi/README.md](openapi/README.md) explains the pipeline, how to review a spec change and how to add an operation.

## Code layout

| Path | Contents |
|---|---|
| `/` | The SDK, package `hyperbolic`. `<service>_gen.go` holds a service and the types only it uses, `shared_gen.go` the shared types, `client_gen.go` the `Client`. Hand-written: `client.go`, `errors.go`, `retry.go`, `pagination.go`, `version.go`. |
| `openapi/` | The pinned spec, the overlay and `sdk.yaml`. |
| `internal/gen/` | The generator and its templates, a separate module. |
| `testdata/responses/` | Sample responses built from the spec. |
| `hack/` | The license header template. |

The SDK is one package at the module root, as Go's [module layout guidance](https://go.dev/doc/modules/layout) recommends for a library. The generator is a separate module, so its dependencies never reach SDK users.

## Pull requests

- One logical change per pull request.
- Every behavior change comes with tests. Code that creates or terminates rentals needs tests for its failure modes.
- Title the pull request `type(scope): summary`, following [Conventional Commits](https://www.conventionalcommits.org/), and write it for the release notes. Pull requests are squash-merged, so the title is also the commit on `main`.
- A breaking change to the Go API needs `!` in the title and a description of what users must change.
- The README's quick start is `Example` in `example_test.go`. Change both together or `TestReadmeQuickStart` fails.
- CI must pass before review.

## Releases

Releases follow [semantic versioning](https://semver.org/). Before 1.0 a minor version may break the Go API, and the release notes call out every breaking change.
