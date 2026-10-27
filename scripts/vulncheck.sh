#!/usr/bin/env bash
# Runs govulncheck and fails when the code reaches a known vulnerability that
# has a fixed version: those must be fixed by bumping the module or the Go
# toolchain. Reachable vulnerabilities without any fix (e.g. the unmaintained
# golang.org/x/crypto/openpgp the Cosmos SDK imports) are listed but do not
# fail the build, since no bump can remove them.
set -euo pipefail

out="$(mktemp)"
trap 'rm -f "${out}"' EXIT

go run golang.org/x/vuln/cmd/govulncheck@latest -format json ./... > "${out}"

# Findings with a function in their trace are reachable from our code.
reachable="$(jq -r 'select(.finding and .finding.trace[0].function)
  | .finding | "\(.osv) \(.fixed_version // "-")"' "${out}" | sort -u)"

if [[ -z "${reachable}" ]]; then
  echo "govulncheck: no reachable vulnerabilities"
  exit 0
fi

fixable="$(awk '$2 != "-"' <<< "${reachable}")"
unfixable="$(awk '$2 == "-"' <<< "${reachable}")"

if [[ -n "${unfixable}" ]]; then
  echo "govulncheck: reachable, no fix available (not failing):"
  sed 's/^/  /' <<< "${unfixable}"
fi
if [[ -n "${fixable}" ]]; then
  echo "govulncheck: reachable with a fix available (bump the module or the toolchain):"
  sed 's/^/  /' <<< "${fixable}"
  exit 1
fi
