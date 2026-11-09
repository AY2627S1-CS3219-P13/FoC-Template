#!/bin/sh
# Print the Pricing policy package that releases publish, relative to order_pricing.
# The selection is tracked in order_pricing/RELEASE_PACKAGE so every commit's image
# is reproducible and switching packages is an ordinary reviewed change.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
package=$(tr -d '[:space:]' < "$root/RELEASE_PACKAGE")
case "$package" in
  "" | /* | *..* | *[!A-Za-z0-9._/-]*)
    echo "order_pricing/RELEASE_PACKAGE must name a relative package directory" >&2
    exit 1 ;;
esac
git -C "$root" ls-files --error-unmatch -- "$package/manifest.json" > /dev/null
printf '%s\n' "$package"
