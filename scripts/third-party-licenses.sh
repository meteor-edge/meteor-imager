#!/usr/bin/env bash
# Writes the licence texts of every module linked into meteor-imager to stdout.
set -euo pipefail
cd "$(dirname "$0")/.."
go list -deps -f '{{if not .Standard}}{{with .Module}}{{.Path}} {{.Version}} {{if .Replace}}{{.Replace.Dir}}{{else}}{{.Dir}}{{end}}{{end}}{{end}}' ./cmd/meteor-imager \
  | sort -u | while read -r path version dir; do
    [[ "$path" == github.com/meteor-edge/meteor-imager ]] && continue
    lic=$(ls "$dir" | grep -iE '^(licen[cs]e|copying)' | head -1 || true)
    if [[ -z "$lic" ]]; then
      echo "missing licence for $path" >&2
      exit 1
    fi
    printf '================================================================\n%s %s\n================================================================\n\n' "$path" "$version"
    cat "$dir/$lic"
    for extra in NOTICE PATCHES.md; do
      [[ -f "$dir/$extra" ]] && { printf '\n--- %s ---\n' "$extra"; cat "$dir/$extra"; }
    done
    printf '\n\n'
  done
printf '================================================================\nGo standard library\n================================================================\n\n'
cat "$(go env GOROOT)/LICENSE"
