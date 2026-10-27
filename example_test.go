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

package hyperbolic_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	hyperbolic "github.com/HyperbolicLabs/hyperbolic-go"
)

// Print the balance and the virtual machines that can be rented now. Neither
// call spends money. The README's quick start is this example.
func Example() {
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

// Rent the cheapest in-stock 8x H100 virtual machine, handling the one
// failure that can bill twice if ignored: a create whose outcome is unknown.
func ExampleRentalsService_Create() {
	ctx := context.Background()
	client, err := hyperbolic.NewClient(os.Getenv("HYPERBOLIC_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}

	options, err := client.Rentals.Options(ctx)
	if err != nil {
		log.Fatal(err)
	}
	var cheapest *hyperbolic.VirtualMachineOption
	for _, o := range options {
		vm, err := o.AsVirtualMachineOption()
		if err != nil || vm.MachineType != hyperbolic.VirtualMachineOptionMachineTypeVirtualMachine {
			continue
		}
		inStock := vm.Enabled && (vm.TotalAvailable == nil || *vm.TotalAvailable > 0)
		if vm.GPUType == hyperbolic.GPUTypeH100 && vm.GPUCount == 8 && inStock &&
			(cheapest == nil || vm.CostPerHourCents < cheapest.CostPerHourCents) {
			cheapest = &vm
		}
	}
	if cheapest == nil {
		log.Fatal("no 8x H100 in stock")
	}

	const label = "train-run-42"
	created, err := client.Rentals.Create(ctx, hyperbolic.CreateRentalParams{
		RentalType: hyperbolic.RentalTypeVirtualMachine,
		GPUType:    hyperbolic.Ptr(hyperbolic.GPUTypeH100),
		GPUCount:   8,
		Region:     hyperbolic.Ptr(cheapest.Region),
		Label:      hyperbolic.Ptr(label),
	})
	if errors.Is(err, hyperbolic.ErrOutcomeUnknown) {
		// The rental may exist. Look it up by label instead of creating
		// another one.
		for r, err := range client.Rentals.ListActiveAll(ctx, hyperbolic.ListActiveRentalsParams{}) {
			if err != nil {
				log.Fatal(err)
			}
			if l, _ := r.Label.Get(); l == label {
				fmt.Println("rental exists:", r.RentalID)
				return
			}
		}
		log.Fatal("the create did not take effect, so it is safe to try again")
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("created rental", created.RentalID)
}

// List every active rental, a page at a time behind the iterator.
func ExampleRentalsService_ListActiveAll() {
	ctx := context.Background()
	client, err := hyperbolic.NewClient(os.Getenv("HYPERBOLIC_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}
	for r, err := range client.Rentals.ListActiveAll(ctx, hyperbolic.ListActiveRentalsParams{}) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(r.RentalID, r.Status, r.GPUCount, r.GPUType)
	}
}
