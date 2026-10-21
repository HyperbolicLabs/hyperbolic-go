<p align="center">
  <a href="https://hyperbolic.ai">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset=".github/assets/HYP-Logo-Full-Light.svg">
      <source media="(prefers-color-scheme: light)" srcset=".github/assets/HYP-Logo-Full-Dark.svg">
      <img alt="Hyperbolic" src=".github/assets/HYP-Logo-Full-Dark.svg" width="300">
    </picture>
  </a>
</p>

# hyperbolic-go

[![Go Reference](https://pkg.go.dev/badge/github.com/HyperbolicLabs/hyperbolic-go.svg)](https://pkg.go.dev/github.com/HyperbolicLabs/hyperbolic-go)
[![CI](https://github.com/HyperbolicLabs/hyperbolic-go/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/HyperbolicLabs/hyperbolic-go/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

The official Go SDK for the [Hyperbolic](https://hyperbolic.ai) API. Use it to rent GPUs, manage storage volumes and SSH keys, and track your balance from Go.

## Requirements

- Go 1.23 or later. CI tests the two most recent Go releases.
- A Hyperbolic API key. Create one under **Settings > API Keys** in the [dashboard](https://app.hyperbolic.ai/settings/api-keys).

## Installation

```console
go get github.com/HyperbolicLabs/hyperbolic-go
```

While the repository is private, tell Go not to look the module up in the public proxy and checksum database, and give Git read access with an SSH key or an HTTPS token (`git config url."https://<token>@github.com/".insteadOf https://github.com/`).

```console
go env -w GOPRIVATE=github.com/HyperbolicLabs/*
```

## Documentation

- [Package reference](https://pkg.go.dev/github.com/HyperbolicLabs/hyperbolic-go): every type and method
- [Hyperbolic documentation](https://docs.hyperbolic.ai): the platform, its GPUs and its API
- [Releases](https://github.com/HyperbolicLabs/hyperbolic-go/releases): what changed in each version

## Versioning

hyperbolic-go follows [semantic versioning](https://semver.org/). Before 1.0 a minor version may change the Go API. The release notes call out every breaking change, and its pull request says what to change. The two most recent Go releases are supported, following Go's [release policy](https://go.dev/doc/devel/release#policy).

## Getting help

- Report SDK bugs and feature requests by [opening an issue](https://github.com/HyperbolicLabs/hyperbolic-go/issues/new/choose).
- For your account, billing or the API itself, contact Hyperbolic support at [support@hyperbolic.ai](mailto:support@hyperbolic.ai) or through the chat in the [dashboard](https://app.hyperbolic.ai).
- Report security vulnerabilities privately, as [SECURITY.md](SECURITY.md) describes, never in a public issue.

## Contributing

Contributions are welcome. [CONTRIBUTING.md](CONTRIBUTING.md) explains the checks and the pull request process. Everyone taking part follows the [Code of Conduct](CODE_OF_CONDUCT.md).

## License

hyperbolic-go is licensed under the [Apache License 2.0](LICENSE). Copyright Hyperbolic Labs, Inc.
