# This file is the project's own.
# Add recipes leveraging provided `do` ready-made recipes, or create your own.
# The import must be kept: it mounts every shared limen task under `just do ...`.
import '.limen/just/main.just'

# The FIRST recipe defined here becomes `just`'s default.
[doc('The shared limen baseline and the Go lane.')]
lint: do::lint::default do::lint::go::default
fix: do::fix::default do::fix::go::default
test: do::test::go::default
security: do::security::default do::security::go::default

# unifictl (docs/unifictl.md), built from this module at the commit checked
# out and run from a machine on the console's LAN; nothing here runs in CI.
# The console's address, site and API key come from the environment:
# UNIFI_URL, UNIFI_SITE, UNIFI_API_KEY, and UNIFI_TLS_FINGERPRINT pins its
# certificate. `plan` exits 2 when something would change, the convention
# `tofu plan -detailed-exitcode` set.
[doc('Build unifictl and run it: `just unifictl plan <dir>`.')]
unifictl +args: build-unifictl
    .build/unifictl {{ args }}

build-unifictl:
    mkdir -p .build
    go build -o .build/unifictl ./cmd/unifictl
