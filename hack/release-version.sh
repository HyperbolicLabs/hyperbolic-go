#!/usr/bin/env bash
# Copyright 2026 Hyperbolic Labs, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Prints the version the next release gets on stdout, and gorelease's report on
# stderr. Usage: hack/release-version.sh [VERSION]
#
# gorelease compares the exported Go API with the last release and suggests
# the smallest version that fits the change. Without VERSION, that suggestion
# is the version. With VERSION, it must not be smaller than the suggestion: it
# may be a pre-release of it (v0.3.0-rc.1) or a larger version (v1.0.0).

set -euo pipefail

requested=${1:-}
root=$(git rev-parse --show-toplevel)
gorelease=(go tool -modfile="$root/internal/tools/go.mod" gorelease)

die() {
	echo "release-version: $*" >&2
	exit 1
}

if [ -n "$requested" ] && ! [[ $requested =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
	die "$requested is not a version like v1.2.3 or v1.2.3-rc.1"
fi
if [ -n "$requested" ] && git rev-parse -q --verify "refs/tags/$requested" >/dev/null; then
	die "$requested is already released"
fi

# The first release has nothing to compare with. (A plain variable, not an
# array: bash 3.2, macOS's, rejects an empty array under set -u.)
base=
if [ -z "$(git tag --list 'v*')" ]; then
	base=-base=none
fi

report=$(cd "$root" && "${gorelease[@]}" ${base:+"$base"}) || {
	echo "$report" >&2
	die "gorelease failed"
}
echo "$report" >&2
suggested=$(sed -n 's/^Suggested version: //p' <<<"$report")
[ -n "$suggested" ] || die "gorelease suggested no version"

if [ -z "$requested" ]; then
	echo "$suggested"
	exit 0
fi

# sort -V orders a pre-release after its release (v0.3.0 before v0.3.0-rc.1),
# so a pre-release of the suggested version passes, as it should.
if [ "$(printf '%s\n%s\n' "$suggested" "$requested" | sort -V | head -n1)" != "$suggested" ]; then
	die "$requested is smaller than $suggested, the smallest version this API change allows"
fi
# gorelease also checks the requested version itself, for example that a new
# major version changes the module path.
(cd "$root" && "${gorelease[@]}" ${base:+"$base"} -version="$requested") >&2 || die "gorelease rejected $requested"
echo "$requested"
