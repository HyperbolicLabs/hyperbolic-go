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

import "runtime/debug"

const modulePath = "github.com/HyperbolicLabs/hyperbolic-go"

// version is the SDK version the program was built with, such as v0.2.0, as
// Go recorded it in the build. A release is only a git tag, so there is no
// constant to keep in step with it. It is sent in the User-Agent header so
// Hyperbolic can see which SDK versions call the API.
var version = moduleVersion(debug.ReadBuildInfo())

// moduleVersion finds the SDK's version in a program's build info. It returns
// "devel" when the build does not know it: in the SDK's own tests, or when a
// replace directive points at a local copy.
func moduleVersion(info *debug.BuildInfo, ok bool) string {
	const unknown = "devel"
	if !ok {
		return unknown
	}
	var m *debug.Module
	if info.Main.Path == modulePath {
		m = &info.Main
	}
	for _, d := range info.Deps {
		if d.Path == modulePath {
			m = d
		}
	}
	if m == nil {
		return unknown
	}
	if m.Replace != nil {
		m = m.Replace
	}
	if m.Version == "" || m.Version == "(devel)" {
		return unknown
	}
	return m.Version
}
