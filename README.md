# go-unifi

A UniFi Network console as files, and `unifictl`, the tool between them. It reads the
controller's own documents over the console's private API, writes them out as YAML, one
directory per kind and one file per document, and makes the controller match the files
again. There is no translation layer: a file holds the controller's keys with the
controller's spellings, and what the controller accepts, the file can say.

```bash
export UNIFI_URL=https://192.168.1.1 # the console; the factory address is the default
export UNIFI_API_KEY=...            # Control Plane → Integrations, on the console
export UNIFI_TLS_FINGERPRINT=...    # the console's certificate, SHA-256, hex
unifictl dump console               # the console into console/, one file per document
unifictl plan console               # what apply would change; exit 2 when something would
unifictl apply console              # make the console match the files
```

[docs/unifictl.md](docs/unifictl.md) is the design: what a file is and how much of a
document it has to state, which keys never appear in one, how references are written,
how the firewall policy order is kept, how a plan is read and an apply checked against
the controller's answer. The files of a console belong with whoever runs it; this
repository holds the tool.

The library is `console`, the slice of the controller's API the tool speaks; `documents`,
the files; and `reconcile`, the plan and the apply between them. `cmd/unifictl` and
`internal/cli` are the binary around them, and another binary wires the same three.

## Installing

Each release carries an archive per platform: macOS on Apple silicon, Linux and Windows
on amd64 and arm64. Download the one for the machine, verify it as below, and put the
`unifictl` inside it on the path.

## Verifying a release

Every archive, its SBOM and `checksums.txt` carry GitHub's build provenance attestation,
and `checksums.txt` a keyless signature by the release workflow at the tag, the one asset
that is a signature and not a subject; the one command covers an archive, its SBOM and the
checksums file alike:

```sh
gh attestation verify unifictl_<version>_linux_amd64.tar.gz --repo duncanite/go-unifi
```

For a verifier with cosign and no `gh`, the signature over `checksums.txt`, then the
checksums themselves:

```sh
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github.com/duncanite/go-unifi/\.github/workflows/release\.yaml@refs/tags/v'
sha256sum -c checksums.txt
```

## Working on it

```bash
just lint             # the shared limen baseline and the Go lanes
just test             # the Go tests, against a fake controller
just security         # the shared scans and the link check
just fix              # repair what `just lint` reports
just unifictl <args>  # build unifictl from the checkout and run it
```

`just --list` shows the rest.
