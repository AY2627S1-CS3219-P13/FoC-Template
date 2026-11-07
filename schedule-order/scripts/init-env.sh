#!/bin/sh
set -eu
# Compatibility entry point: scheduling now uses Order's environment.
sh "$(dirname "$0")/../../order-service/scripts/init-env.sh"
