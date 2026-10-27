/*
Copyright 2026 Hyperbolic Labs, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package hyperbolic is the Go SDK for the Hyperbolic API.
//
// Its operations and types are generated from the API's published OpenAPI
// spec (https://api.hyperbolic.ai/v2/openapi.json). A [Client] groups them by
// area: [Client.Rentals], [Client.StorageVolumes], [Client.SSHKeys],
// [Client.Balances], [Client.APIKeys] and [Client.Users]. Each method takes a
// [context.Context] and, when the operation has inputs, a ...Params struct.
//
// # Getting started
//
//	client, err := hyperbolic.NewClient(os.Getenv("HYPERBOLIC_API_KEY"))
//	if err != nil { ... }
//	created, err := client.Rentals.Create(ctx, hyperbolic.CreateRentalParams{
//		RentalType: hyperbolic.RentalTypeVirtualMachine,
//		GPUType:    hyperbolic.Ptr(hyperbolic.GPUTypeH100),
//		GPUCount:   1,
//	})
//
// # Things the API does that are easy to get wrong
//
//   - Creating a rental is not idempotent and the API has no idempotency key.
//     See "Create safety" below.
//   - A rental is identified by its string RentalID, which terminate,
//     reboot and the lookup take.
//   - Money is in cents, including CostPerHour on a rental's current term
//     (3192 == $31.92/hr).
//   - A create returns the rental without its live status. Status, instances
//     and SSH endpoints come from [RentalsService.Get] or from listing.
//   - There are no webhooks. Status is observed by polling.
//
// # Create safety
//
// If a call that is not safe to repeat, such as creating a rental, fails in a
// way that leaves its outcome unknown (a 5xx response, a timeout, or a
// dropped connection after the request was sent), it may have taken effect
// even though it returned an error. The client never retries it in that
// situation. Instead it returns an error matching [ErrOutcomeUnknown]:
//
//	created, err := client.Rentals.Create(ctx, params)
//	if errors.Is(err, hyperbolic.ErrOutcomeUnknown) {
//		// Look the rental up (for example by label) before trying again.
//	}
//
// Calls that are safe to repeat are retried on transient failures according
// to the client's [RetryPolicy]. Calls the server provably did not process
// (HTTP 429, or a connection that was never made) are retried for every kind
// of call.
//
// # Types
//
// Optional request fields are pointers. Set them with [Ptr]. Nullable
// response fields are nullable.Nullable values from
// github.com/oapi-codegen/nullable. Unions have a Discriminator method and an
// As method per variant. Enums are typed strings, and a value the SDK does not
// know yet still decodes.
//
// # Testing
//
// Package hyperbolictest is an in-memory fake of the API with fault
// injection, for testing code that uses this package.
package hyperbolic
