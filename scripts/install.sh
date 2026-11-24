#!/usr/bin/env bash
# Installs the release this script sits beside into the prusa_connect
# container, then restarts the container so the server starts the module.
# Run on the Pi with sudo: the container is only reachable through docker.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
version=$(sed -n 's/^version: //p' "$here/module.yaml")
module=github.com/robeeejay/prusa-tasmota-lights
inside=/tmp/tasmota-lights-$version

# /tmp is the container's one writable scratch volume; its root is read-only.
docker exec prusa_connect rm -rf "$inside"
docker exec prusa_connect mkdir -p "$inside"
for file in module.yaml checksums.txt tasmota-lights_linux_arm64; do
  docker cp "$here/$file" "prusa_connect:$inside/$file"
done

# -approve grants what module.yaml asks for (observe) and switches it on.
docker exec prusa_connect /prusa_connect module add "$module@$version" -from "$inside" -approve
docker exec prusa_connect rm -rf "$inside"

# A running server reads the install record at its next start.
docker restart prusa_connect
