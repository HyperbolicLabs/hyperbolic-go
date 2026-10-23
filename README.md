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

The SDK is generated from Hyperbolic's published [OpenAPI specification](https://api.hyperbolic.ai/v2/openapi.json), so it covers the entire API and stays in step with it.

## Requirements

- Go 1.24 or later. CI tests the two most recent Go releases.
- A Hyperbolic API key. Create one under **Settings > API Keys** in the [dashboard](https://app.hyperbolic.ai/settings/api-keys).

## Installation

```console
go get github.com/HyperbolicLabs/hyperbolic-go
```

While the repository is private, tell Go not to look the module up in the public proxy and checksum database, and give Git read access with an SSH key or an HTTPS token (`git config url."https://<token>@github.com/".insteadOf https://github.com/`).

```console
go env -w GOPRIVATE=github.com/HyperbolicLabs/*
```

## Usage

### Quick start

This program prints your balance and the virtual machines you can rent now. Neither call spends money.

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	hyperbolic "github.com/HyperbolicLabs/hyperbolic-go"
)

func main() {
	ctx := context.Background()
	client, err := hyperbolic.NewClient(os.Getenv("HYPERBOLIC_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}

	balance, err := client.Balances.Get(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("balance: $%.2f\n", balance.BalanceCents/100)

	options, err := client.Rentals.Options(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, o := range options {
		v, err := o.ValueByDiscriminator()
		if err != nil {
			continue // a machine type this version of the SDK does not know
		}
		if vm, ok := v.(hyperbolic.VirtualMachineOption); ok && vm.Enabled {
			fmt.Printf("%d x %s in %s: $%.2f/hour\n", vm.GPUCount, vm.GPUType, vm.Region, vm.CostPerHourCents/100)
		}
	}
}
```

### Renting a GPU

```go
created, err := client.Rentals.Create(ctx, hyperbolic.CreateRentalParams{
	RentalType: hyperbolic.RentalTypeVirtualMachine,
	GPUType:    hyperbolic.Ptr(hyperbolic.GPUTypeH100),
	GPUCount:   1,
	Region:     hyperbolic.Ptr("us-central-1"),
	Label:      hyperbolic.Ptr("train-run-42"),
})
if err != nil {
	return err // see "Retries and create safety" before retrying
}

// The API has no webhooks: poll until the rental is running.
found, err := client.Rentals.Get(ctx, hyperbolic.GetRentalParams{RentalID: created.RentalID})
if err != nil {
	return err
}
if active, err := found.AsActiveRentalLookup(); err == nil && active.Rental.Status == hyperbolic.RentalStatusRunning {
	for _, ep := range active.Rental.SSHEndpoints {
		fmt.Printf("ssh %s@%s -p %d\n", ep.User, ep.Host, ep.Port)
	}
}

// Stop paying.
if _, err := client.Rentals.Terminate(ctx, hyperbolic.TerminateRentalParams{RentalID: created.RentalID}); err != nil {
	return err
}
```

A rental is identified by its string `RentalID`. Money is in cents, including `CostPerHour` on a rental's current term.

### Services

The client groups the API's operations into services named after the specification's tags.

| Service | Methods |
|---|---|
| `Rentals` | `Options`, `Create`, `Get`, `ListActive`, `ListHistory`, `HasHistory`, `ActiveSummary`, `Update`, `Terminate`, `RebootInstance`, `AttachSSHKey`, `DetachSSHKey`, and reservations: `ReserveOptions`, `RequestReservation`, `ConversionQuote`, `ConvertToReserved`, `ExtensionQuote`, `ExtendReserved` |
| `StorageVolumes` | `Options`, `List`, `Create`, `Update`, `Attach`, `Terminate` |
| `SSHKeys` | `List`, `ListForRental`, `Create`, `Update`, `Delete` |
| `Balances` | `Get` |
| `APIKeys` | `List` |
| `Users` | `Me`, `UpdateMe`, `VerifyPhoneNumber`, `DeleteMe` |

Each method takes a `context.Context` and, when the operation has inputs, one `...Params` struct. Its doc comment names the HTTP call it makes. Deprecated operations are left out.

### Configuration

`NewClient` takes options.

```go
client, err := hyperbolic.NewClient(apiKey,
	hyperbolic.WithUserAgent("my-scheduler/1.4.0"),
	hyperbolic.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}),
	hyperbolic.WithRetryPolicy(hyperbolic.NoRetries()),
)
```

| Option | Default | Use it to |
|---|---|---|
| `WithBaseURL` | `https://api.hyperbolic.ai` | Point the client at another endpoint, such as a test server. |
| `WithHTTPClient` | An `http.Client` with a 30 second timeout | Set your own transport, proxy or timeout. |
| `WithUserAgent` | `hyperbolic-go/<version>` | Name your application in the `User-Agent` header, ahead of the SDK's own token. |
| `WithRetryPolicy` | `DefaultRetryPolicy()`, 3 attempts | Tune retries, or turn them off with `NoRetries()`. |

The base URL must use https, except for loopback test servers, so an API key never crosses a network unencrypted. The key never appears in `%v` or `%#v` output.

### Errors

Every API failure is an `*hyperbolic.APIError` with the HTTP status, the machine-readable `Code`, the message and the `Issues` the API listed. Helpers cover the common checks.

| Helper | True when |
|---|---|
| `IsOutcomeUnknown(err)` | A call that is not safe to repeat may or may not have taken effect |
| `IsNotFound(err)` | The object does not exist, including a lookup the API answered with null |
| `IsUnauthorized(err)` | The API key was rejected |
| `IsRateLimited(err)` | The request hit a rate limit. `APIError.RetryAfter` says how long to wait |
| `HasCode(err, hyperbolic.CodeInsufficientBalance)` | The API returned that error code |

### Retries and create safety

Calls that are safe to repeat are retried on 429, 502, 503, 504 and transport errors, with exponential backoff and jitter, honoring `Retry-After`. If the server asks to wait longer than `RetryPolicy.MaxDelay`, the client returns the error instead of blocking, so a controller can requeue.

Creating a rental is not idempotent, and the API has no idempotency key. A create that fails after the server applied it, through a 5xx, a timeout or a dropped connection, would rent the GPUs twice if retried blindly. The client retries such a call only on a 429 or when the connection never opened, the two cases where the server provably did not process it. Any other failure returns an error matching `ErrOutcomeUnknown`.

```go
created, err := client.Rentals.Create(ctx, params)
switch {
case errors.Is(err, hyperbolic.ErrOutcomeUnknown):
	// The rental may exist. Find it by label before creating another one.
	for r, err := range client.Rentals.ListActiveAll(ctx, hyperbolic.ListActiveRentalsParams{}) {
		...
	}
case err != nil:
	// Definite failure: nothing was created.
}
```

Set a `Label` you can recognize on every create so this lookup is possible. Which calls are safe to repeat is declared per operation in [`openapi/sdk.yaml`](openapi/sdk.yaml) and tested for each one.

### Pagination

List operations that return pages also have an `...All` method, a Go iterator over every item that fetches pages as it goes.

```go
for r, err := range client.Rentals.ListActiveAll(ctx, hyperbolic.ListActiveRentalsParams{}) {
	if err != nil {
		return err
	}
	fmt.Println(r.RentalID, r.Status)
}
```

### Types

- Optional request fields are pointers. Set them with `hyperbolic.Ptr(v)`.
- Nullable response fields are `nullable.Nullable[T]`. `Get()` returns the value and `IsNull()` reports an explicit null.
- Unions such as a rental's `CurrentTerm` have `Discriminator()`, `ValueByDiscriminator()` and an `As...` method per variant, such as `AsOnDemandTerm`.
- Enums are typed strings with a constant per known value, such as `RentalStatusRunning`. A value the SDK does not know yet still decodes, and `Valid()` reports whether it is known.

## Documentation

- [Package reference](https://pkg.go.dev/github.com/HyperbolicLabs/hyperbolic-go): every type and method, with runnable examples
- [Hyperbolic documentation](https://docs.hyperbolic.ai): the platform, its GPUs and its API
- [OpenAPI specification](https://api.hyperbolic.ai/v2/openapi.json): the API contract this SDK is generated from
- [Releases](https://github.com/HyperbolicLabs/hyperbolic-go/releases): what changed in each version

## Versioning

hyperbolic-go follows [semantic versioning](https://semver.org/). Before 1.0 a minor version may change the Go API. The release notes call out every breaking change, and its pull request says what to change. The two most recent Go releases are supported, following Go's [release policy](https://go.dev/doc/devel/release#policy).

## Getting help

- Report SDK bugs and feature requests by [opening an issue](https://github.com/HyperbolicLabs/hyperbolic-go/issues/new/choose).
- For your account, billing or the API itself, contact Hyperbolic support at [support@hyperbolic.ai](mailto:support@hyperbolic.ai) or through the chat in the [dashboard](https://app.hyperbolic.ai).
- Report security vulnerabilities privately, as [SECURITY.md](SECURITY.md) describes, never in a public issue.

## Contributing

Contributions are welcome. Most of the SDK is generated, so a change to its shape belongs in the specification, the overlay or the templates in [`openapi/`](openapi/) and [`internal/gen/`](internal/gen/), never in a `_gen.go` file. [CONTRIBUTING.md](CONTRIBUTING.md) explains the pipeline, the checks and the pull request process. Everyone taking part follows the [Code of Conduct](CODE_OF_CONDUCT.md).

## License

hyperbolic-go is licensed under the [Apache License 2.0](LICENSE). Copyright Hyperbolic Labs, Inc.
