# openapi

What the SDK is generated from.

| File | What it is | Who changes it |
|---|---|---|
| `openapi.json` | A pinned copy of the published spec, https://api.hyperbolic.ai/v2/openapi.json, indented so changes read as diffs. | `make spec-sync` and the daily sync job. Never by hand. |
| `overlay.yaml` | An [OpenAPI Overlay](https://spec.openapis.org/overlay/v1.0.0) with fixes the published spec does not have yet. | By hand, one group per problem below. |
| `sdk.yaml` | The SDK's surface: the operations it exposes, the Go name of each, whether it is safe to repeat, how it paginates, and Go names for the enums and unions the spec repeats inline. | By hand. |

`make generate` applies the overlay, checks the spec against `sdk.yaml`, and writes the following files.

- A file per service, such as `rentals_gen.go`, with the service and the types only it uses. `shared_gen.go` has the types several services use and `client_gen.go` the `Client`. Types come from oapi-codegen and services from `internal/gen/templates`.
- `hyperbolictest/internal/server/`, the test server's routes and parameter handling, kept internal so they stay out of its public API.
- `operations_gen_test.go` and `testdata/responses/`, a test per operation and sample responses built from the spec. Each method must decode every sample without losing anything.

Every generated file starts with the license header in `hack/boilerplate.go.txt` and is marked generated in `.gitattributes`, so GitHub collapses it in pull request diffs. CI fails when the committed files differ from what `make generate` writes.

## When the published spec changes

A daily job runs `make spec-sync`, which pulls the spec and regenerates. It opens a pull request with what changed for API clients according to [oasdiff](https://github.com/oasdiff/oasdiff), whether anything breaks them, and the test results. What to look for:

- A new operation fails generation until `sdk.yaml` gives it a service, a method name and whether it is safe to repeat. The name becomes public API, so choose it deliberately.
- A deprecated operation fails generation while `sdk.yaml` still lists it. Removing it from `sdk.yaml` is a breaking change for the SDK.
- A breaking change fails the API changes check. Merging it needs the `breaking-api-change` label and a description of what users must change. Before 1.0 it ships in the next minor version.
- An overlay action that no longer matches fails generation. Usually the spec now carries the fix, so delete the action and its row below.

## Adding an operation

1. Add it under its service in `sdk.yaml` with `operation`, the operationId, and `id`, the Go name its types derive from. Set `idempotent` if the HTTP method's default is wrong for it. GET, PUT and DELETE default to true and POST to false. Set `paginate` if it returns pages.
2. Run `make generate`. The generated test calls the new method with every sample response and checks its retry behavior, and `make test` runs it.
3. If `hyperbolictest` should model it, implement its handler on the test server's `api` type. Otherwise it answers 501.

## Fixes for the published spec

These problems in the published spec affect every client generated from it. Each is worked around here until the spec is fixed, and the last column says where.

| # | Problem | Where | Effect on typed clients | Suggested fix | Worked around in |
|---|---|---|---|---|---|
| 1 | Whole numbers typed `number` | 37 fields: counts such as `gpuCount`, `cpuCount` and `vcpuCount`, sizes such as `ramGb`, `storageGb`, `gpuMemoryGb` and `maxSizeInGb`, ports in `SshEndpoint.port`, `PortMapping.*Port` and `exposedPorts`, `termPeriodDays`, `totalAvailable` | Generated as floating point, `float32` with oapi-codegen's defaults | `type: integer`, which is `z.number().int()` in zod | `overlay.yaml` |
| 2 | Ids accept `integer \| string` | `rentalId` and `rentalIds` in 10 request fields, storage volume ids in 4 | Generated as an opaque union instead of a string, although responses only ever return strings | `type: string`. The integer variant of `rentalId` is already marked deprecated | `overlay.yaml` |
| 3 | Regions are closed enums, and in requests a union of two | `region` in create bodies, rental options, responses and list filters | Clients that check enums reject regions added after their release, and requests get an opaque union | `type: string` with the known regions as `examples` | `overlay.yaml` |
| 4 | Enums repeated inline | `gpuType` in 6 places, `gpuFormFactor` in 5, `connectionType` in 5, `rentalType` in 4, `termType`, `ethernetVariant`, and rental and storage volume `status` | One type per place they appear, such as `RentalGPUType` and `HistoricalRentalGPUType` | Named schemas in `components`, such as `GPUType` and `RentalStatus`, referenced with `$ref` | `sdk.yaml` `enums` |
| 5 | Unions without a discriminator | A rental's `currentTerm`, told apart by `termType`, rental options by `machineType`, and a create's `storageVolume` by `isNew` | Clients must try each variant in turn | Named variant schemas and a `discriminator` | `sdk.yaml` `unions` |
| 6 | Inline request and response schemas | 18 request bodies, 6 responses | Type names derived from operation ids, such as `OndemandCreateRentalJSONBody` | Named schemas such as `CreateRentalRequest` and `RentalOption` | `sdk.yaml` method ids |
| 7 | "Not found" as 200 with a `null` body | `GET /v2/on-demand/rental` | Clients must special-case `null` | 404 with the error envelope, as other endpoints do | `sdk.yaml` `nullIsNotFound` |
| 8 | Error codes not documented | The error envelopes list only generic codes such as `BAD_REQUEST` | Clients rely on codes observed in practice: `INSUFFICIENT_BALANCE`, `NO_PUBLIC_KEY`, `REGION_DISABLED`, `CUSTOM_PORTS_NOT_SUPPORTED_FOR_REGION`, `FAILED_TO_CALCULATE_COST_PER_HOUR`, `RENTAL_NOT_FOUND` | Document each operation's codes as an enum on `code` | `errors.go` constants |
| 9 | No idempotency key on creates | `POST /v2/on-demand/rentals`, storage volumes, reservations, conversions | A create whose outcome is unknown cannot be retried safely, only looked up | Accept an `Idempotency-Key` header and return the first result for a repeated key | `ErrOutcomeUnknown` in `client.go` |
| 10 | Label length not documented | `label` on rental create and update | Clients find the limit, 40 characters in practice, only by being rejected | `maxLength` on `label` | Not worked around. Callers trim |
| 11 | No request id | Responses | Support cannot match a client's error to a server log | Return and document an `X-Request-Id` header | Not worked around |
