# Releases

## Versioning

hyperbolic-go follows [semantic versioning](https://semver.org/) and Go's [module version numbering](https://go.dev/doc/modules/version-numbers).

| Release | Contains |
|---|---|
| Patch, `v0.1.z` | Fixes. The exported Go API is unchanged. |
| Minor, `v0.y.0` | New API such as operations, types and fields. Before 1.0, also breaking changes, each called out in the release notes. |
| Major, `vX.0.0` | Breaking changes after 1.0. A major version above 1 changes the import path to `github.com/HyperbolicLabs/hyperbolic-go/vX`, and the maintainers decide when. |
| Pre-release, `vX.Y.Z-rc.N` | A candidate for testing. |

The version follows from what changed. [gorelease](https://pkg.go.dev/golang.org/x/exp/cmd/gorelease) compares the exported Go API with the previous release and suggests the smallest version that fits. A release can use a larger version, such as `v1.0.0`, or a pre-release of the suggested one, but never a smaller one.

A spec change can change the Go API, because the SDK is generated from the spec. The pull request that brings it in says what changed for API clients, and CI fails on any pull request that breaks the Go API unless it is labeled `breaking-api-change`.

## Supported Go versions

The two most recent Go releases, following Go's [release policy](https://go.dev/doc/devel/release#policy). CI tests both, and the `go` line in `go.mod` never asks for more than the older one.

## What a release is

A tag `vX.Y.Z` on `main` and a GitHub Release whose notes list the pull requests merged since the previous release, breaking changes first. There are no binaries. Go fetches the module from the tag, and the SDK reads its version from the build, so a release changes no file.

The Go module proxy and checksum database keep every version forever once anyone fetches it. That is why a tag is created only after the checks pass, and never by hand.

## Cutting a release

1. Check that CI passed on the commit of `main` to release. `make release-version` previews the version it would get.
2. Run the **Release** workflow on `main` from Actions. Leave the version empty to release the suggested one, or enter a pre-release of it, such as `v0.3.0-rc.1`, or a larger version, such as `v1.0.0`.
3. An owner approves the run in the `release` environment.

The workflow stops before tagging if CI did not pass on the commit, if the version already exists, or if the version is smaller than the API change allows. Otherwise it creates the tag and the GitHub Release.

While the module is private, `make release-version` needs `GOPRIVATE` and Git credentials, like any other user of the module. Before the first release it compares against nothing and suggests `v0.1.0`.

## Withdrawing a release

Never move or delete a published tag, because a moved tag breaks builds that already fetched it. To withdraw a bad version, release a new one whose `go.mod` [retracts](https://go.dev/ref/mod#go-mod-file-retract) it.

```
retract v0.3.1 // Rentals.Create sent the wrong region.
```
