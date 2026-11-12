# go-unifi

A UniFi Network console as files, and `unifictl`, the tool between them: it reads the
controller's own documents over its private API, writes them out as YAML, one directory per
kind, and makes the controller match the files again. No translation layer: a file holds the
controller's keys with the controller's spellings, and what the controller accepts, the file
can say.

```bash
export UNIFI_API_KEY=...            # Control Plane → Integrations, on the console
export UNIFI_TLS_FINGERPRINT=...    # the console's certificate, SHA-256, hex
just unifictl dump console          # the console into console/, one file per document
just unifictl plan console          # what apply would change; exit 2 when something would
just unifictl apply console         # make the console match the files
```

[docs/unifictl.md](docs/unifictl.md) is the design: what a file is, what the tool prunes,
refuses and resolves, how a plan is read and an apply checked against the controller's answer.
The files of a particular console live with that console's owner, not here.

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
just lint      # the shared limen baseline and the Go lanes
just test      # the Go tests
just security  # the shared scans and the link check
just fix       # repair what `just lint` reports
```

`just --list` shows the rest.
