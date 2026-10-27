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
	"encoding/json"
	"strings"
	"testing"

	hyperbolic "github.com/HyperbolicLabs/hyperbolic-go"
)

// documentedRental is a rental in the shape of the API reference's example
// response.
const documentedRental = `{
  "rentalId": "rnt_8817", "name": "conscious-bougainvillea-oryx", "label": "train-run-42",
  "rentalType": "virtual-machine", "region": "us-central-1", "gpuType": "h100", "gpuCount": 8,
  "gpuFormFactor": "sxm5", "connectionType": "infiniband", "ethernetVariant": null,
  "sshEndpoints": [{"host": "203.0.113.10", "port": 22, "user": "ubuntu"}],
  "networkInterfaces": [{"publicIp": "203.0.113.10", "privateIp": "10.0.0.10"}],
  "portMappings": [], "exposedPorts": [8080], "supportsCustomPorts": true,
  "terminationReason": null,
  "currentTerm": {"effectiveAt": "2025-01-31T12:00:00.000Z", "termType": "on-demand", "costPerHour": 2200,
    "reservedUntil": null, "reservationEndsAt": null, "termPeriodDays": null, "discountPercentage": null, "originalCostPerHour": null},
  "instances": [{"instanceRef": "node-0", "phase": "Running", "ssh": "ubuntu@203.0.113.10:22", "internalIp": "10.0.0.10", "supportsReboot": true}],
  "status": "Running"
}`

func TestDecodesDocumentedRental(t *testing.T) {
	var r hyperbolic.RentalWithStatus
	if err := json.Unmarshal([]byte(documentedRental), &r); err != nil {
		t.Fatal(err)
	}
	name, _ := r.Name.Get()
	label, _ := r.Label.Get()
	if r.RentalID != "rnt_8817" || name != "conscious-bougainvillea-oryx" || label != "train-run-42" ||
		r.Status != hyperbolic.RentalStatusRunning || r.ConnectionType != hyperbolic.ConnectionTypeInfiniband ||
		r.GPUFormFactor != hyperbolic.GPUFormFactorSxm5 || len(r.ExposedPorts) != 1 || r.ExposedPorts[0] != 8080 {
		t.Fatalf("decoded = %+v", r)
	}
	if r.Instances == nil || len(*r.Instances) != 1 || !(*r.Instances)[0].SupportsReboot {
		t.Fatalf("instances = %+v", r.Instances)
	}
	if !r.EthernetVariant.IsNull() || !r.TerminationReason.IsNull() {
		t.Error("explicit nulls must decode as null")
	}
	if kind, err := r.CurrentTerm.Discriminator(); err != nil || kind != "on-demand" {
		t.Fatalf("term kind = %q, err %v", kind, err)
	}
	term, err := r.CurrentTerm.AsOnDemandTerm()
	if err != nil || term.CostPerHour != 2200 {
		t.Fatalf("term = %+v, err %v", term, err)
	}
}

// Re-encoding keeps explicit nulls and leaves out optional fields that were
// absent, so a rental decoded and encoded again says what the API said.
// Required fields are always encoded, because the API always sends them.
func TestNullAndUnsetSurviveARoundTrip(t *testing.T) {
	var r hyperbolic.RentalWithStatus
	if err := json.Unmarshal([]byte(documentedRental), &r); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{`"ethernetVariant":null`, `"label":"train-run-42"`, `"costPerHour":2200`, `"termType":"on-demand"`} {
		if !strings.Contains(out, want) {
			t.Errorf("re-encoded rental lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"actualResources"`) {
		t.Errorf("an absent optional field must stay absent:\n%s", out)
	}
}

func TestUnknownEnumValuesDecode(t *testing.T) {
	var r hyperbolic.RentalWithStatus
	if err := json.Unmarshal([]byte(`{"rentalId":"rnt_a","status":"Hibernating","gpuType":"z900"}`), &r); err != nil {
		t.Fatalf("a value the SDK does not know yet must still decode: %v", err)
	}
	if r.Status.Valid() || r.GPUType != "z900" {
		t.Fatalf("decoded = %+v", r)
	}
}
