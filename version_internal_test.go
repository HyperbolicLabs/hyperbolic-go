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

package hyperbolic

import (
	"runtime/debug"
	"testing"
)

func TestModuleVersion(t *testing.T) {
	sdk := func(v string, replace *debug.Module) *debug.Module {
		return &debug.Module{Path: modulePath, Version: v, Replace: replace}
	}
	app := debug.Module{Path: "example.com/app", Version: "(devel)"}
	tests := []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{"a dependency", &debug.BuildInfo{Main: app, Deps: []*debug.Module{{Path: "example.com/other", Version: "v1.0.0"}, sdk("v0.2.0", nil)}}, true, "v0.2.0"},
		{"a pseudo-version", &debug.BuildInfo{Main: app, Deps: []*debug.Module{sdk("v0.2.1-0.20261021120000-0123456789ab", nil)}}, true, "v0.2.1-0.20261021120000-0123456789ab"},
		{"replaced by a fork", &debug.BuildInfo{Main: app, Deps: []*debug.Module{sdk("v0.2.0", &debug.Module{Path: "example.com/fork", Version: "v0.2.0-fork.1"})}}, true, "v0.2.0-fork.1"},
		{"replaced by a local copy", &debug.BuildInfo{Main: app, Deps: []*debug.Module{sdk("v0.2.0", &debug.Module{Path: "../hyperbolic-go"})}}, true, "devel"},
		{"the main module", &debug.BuildInfo{Main: *sdk("(devel)", nil)}, true, "devel"},
		{"not in the build", &debug.BuildInfo{Main: app}, true, "devel"},
		{"no build info", nil, false, "devel"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := moduleVersion(tt.info, tt.ok); got != tt.want {
				t.Errorf("moduleVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}
