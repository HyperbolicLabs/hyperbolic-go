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
	"os"
	"strings"
	"testing"
)

// TestReadmeQuickStart keeps the README's quick start the same program as
// Example, so the compiler checks the code users copy first.
func TestReadmeQuickStart(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	example, err := os.ReadFile("example_test.go")
	if err != nil {
		t.Fatal(err)
	}
	got := between(t, string(readme), "func main() {\n", "\n}\n```")
	want := between(t, string(example), "func Example() {\n", "\n}\n")
	if got != want {
		t.Errorf("the README's quick start differs from Example in example_test.go")
	}
}

// between returns the text of s after start and before the next end.
func between(t *testing.T, s, start, end string) string {
	t.Helper()
	_, after, ok := strings.Cut(s, start)
	if !ok {
		t.Fatalf("no %q", start)
	}
	body, _, ok := strings.Cut(after, end)
	if !ok {
		t.Fatalf("no %q after %q", end, start)
	}
	return body
}
