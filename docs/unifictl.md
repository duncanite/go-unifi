# unifictl

`unifictl` keeps a UniFi Network console and a directory of YAML files in step. `dump`
writes the console's configuration out as files, `plan` shows where the files and the
console differ, and `apply` changes the console until it matches the files.

The files hold the controller's own documents, key for key, with the controller's
spellings. There is no schema of the tool's own in between: whatever the controller
accepts, a file can say, and a field the controller adds in a new release can be used the
day it appears.

The tool talks to the Network application through the console's private API, the one its
web UI uses: version 1 endpoints under `api/s/<site>/` and version 2 endpoints under
`v2/api/site/<site>/`, both behind the UniFi OS proxy at `/proxy/network/`.

## Commands

```
unifictl dump  <dir>   # write every managed document the console holds into <dir>
unifictl plan  <dir>   # print what apply would change
unifictl apply <dir>   # make the console match the files
```

`dump` refuses a directory that already has files in it unless `--force` is given; with
it, every file is rewritten from the console ("Dump over existing files", below).

Configuration comes from the environment, never from the files:

| variable                | meaning                                                                | default                                   |
| ----------------------- | ---------------------------------------------------------------------- | ----------------------------------------- |
| `UNIFI_URL`             | the console: scheme, host, and the port when it is not 443             | `https://192.168.1.1`, the factory address |
| `UNIFI_SITE`            | the Network site                                                       | `default`                                 |
| `UNIFI_API_KEY`         | an API key minted on the console under Control Plane → Integrations    | required                                  |
| `UNIFI_TLS_FINGERPRINT` | the SHA-256 fingerprint of the console's certificate, in hex           | none                                      |

The console's certificate is self-signed. With `UNIFI_TLS_FINGERPRINT` set, the
connection is accepted only when the certificate matches it, and the system roots are not
consulted. Without it, the system roots apply, which a self-signed certificate fails.
`--insecure` accepts any certificate; it is for the run that reads the fingerprint off a
console the first time, or after its certificate has changed.

Exit statuses:

| status | meaning                                                      |
| ------ | ------------------------------------------------------------ |
| 0      | nothing to do, or done                                       |
| 1      | an error, or an apply the controller did not take in full    |
| 2      | `plan` found something that would change                     |

## Why not the Terraform provider

The Terraform provider for UniFi translates the controller's documents into a schema of
its own, and the translation is where the trouble is: the schema lacks fields the
controller takes (shutting a port, the WAN MAC override, among others), spells some
differently from the controller, invents defaults, and its plan cannot say whether a value
was stated or merely observed. `unifictl` has no translation layer, so none of that
arises: a file says what the controller says.

## The files

### Layout

A directory per collection, named as the controller names it, holding one YAML file per
document, named after the document's identity:

| collection       | identity | controller endpoint                                       |
| ---------------- | -------- | --------------------------------------------------------- |
| `networkconf`    | `name`   | `rest/networkconf`                                        |
| `wlanconf`       | `name`   | `rest/wlanconf`                                           |
| `portforward`    | `name`   | `rest/portforward`                                        |
| `setting`        | `key`    | `rest/setting`, written through `set/setting/<key>`       |
| `device`         | `mac`    | `stat/device`, written through `rest/device/<_id>`        |
| `firewallzone`   | `name`   | v2 `firewall/zone`, written at `firewall/zone/<_id>`      |
| `firewallpolicy` | `name`   | v2 `firewall-policies`, written at `firewall-policies/<_id>` |
| `apgroup`        | `name`   | v2 `apgroups`, written at `apgroups/<_id>`                |

The version 1 endpoints answer inside a `data` envelope; the version 2 ones answer with a
plain array.

### A file is partial

The keys a file states are enforced; the keys it leaves out stay as the controller has
them. This holds at every depth. A map inside the file is partial the same way, key by
key, so a firewall policy's `source` may state its zone and networks and leave the flags
the controller adds alone. A list the controller keys by entry (`port_overrides` by
`port_idx`, `radio_table` by `name`) is merged entry by entry under the same rule: an
entry in the file is enforced key by key, an entry absent from the file is left as it is,
and an entry without its key is refused.

`dump` writes every key the controller holds, so a dumped file is complete and recreates
its document as it was. Trimming a file down to the keys that matter is a choice made
file by file, and the file's comments are the place to say why.

### Dump over existing files

`dump --force` rewrites every file from the console. Comments survive it: a comment on a
key stays with the key, a comment on a list entry with the entry, a block at the top of
the file and a closing line stay where they were, and `dump` names any comment whose key
the console no longer has. Trimming does not survive it: the file comes back complete.

A file for a document that has since been renamed or deleted on the console is removed,
so that it cannot come back later as a create. A file that is not a document (a `.yml`
extension, a stray file) is left where it is by `dump`, and refused, not skipped, by
`plan` and `apply`.

### Keys that are never in a file

Three kinds of key never appear in a file, at the top or anywhere below it. `dump` drops
them and `plan` refuses a file that states one.

- **The controller's own.** `_id`, `site_id`, `external_id`, `short_id`, the `attr_*`
  flags, `public_key` (the site-to-site VPN key the controller generated; its private
  half is an `x_` key), the `predefined` flag on built-in firewall policies, and what the
  controller reports into a configuration document rather than takes from it: a radio's
  capabilities (`radio_caps`, `max_txpower`, `nss`, ...), `last_alert_id`,
  `analytics_disapproved_for`, a policy's `hits` and `last_hit`, and the `origin_id` and
  `cloud_template` the console cross-references with.
- **Secrets.** Every `x_*` key (WLAN passphrases, authentication keys) and the few the
  controller leaves in the clear: `psk`, `sae_psk`, `private_preshared_keys`,
  `utm_token`. `apply` sends the controller its own current values back, so a passphrase
  set once in the UI survives every apply. A WLAN created from a file has no passphrase
  until one is set in the UI.
- **A device's runtime state.** `stat/device` returns the device as it is, uptime and
  counters included; a device file keeps the configuration keys only.

### Documents that have no file

Three kinds of document are neither written by `dump` nor listed as unmanaged by `plan`:

- the console's own, generated or measured there: `peer_to_peer`, the pairing it made
  itself, and `provider_capabilities`, its last speed test;
- a document the controller derives from another: the policy it writes for each port
  forward and for each isolated network, marked by `origin_type`; it changes when the
  document it derives from does;
- a setting that consists of its key and nothing else, whose file would enforce nothing.

`plan` refuses a file for the first two. The built-in firewall zones, marked by
`zone_key`, are the console's own in the same way: nothing on them is a file's to set.

### References by name

A file names the objects it points at; it does not carry their ids. Every key that points
at a network (`networkconf_id`, `native_networkconf_id`, `voice_networkconf_id`,
`mgmt_network_id`, `dot1x_fallback_networkconf_id`, the settings' `*_network_ids` lists and
`enabled_networks`, and the keys of the LED colour list), at an AP group
(`ap_group_ids`), a user group (`usergroup_id`), a RADIUS profile (`radiusprofile_id`) or
a firewall zone (`firewall_zone_id`) is written by `dump` as the name of the object it
points at, and `plan` resolves the name back to the id, failing on a name the console does
not know. A policy's `source` and `destination` name their zone, networks and groups the
same way.

Two values pass through as they are: the controller's own words at a reference (the empty
string on a port with no network, `none` in the LED list), and a raw id of 24 hex digits.
`dump` writes a raw id only when it has no name for what is pointed at, so a file carrying
one points at something the console no longer lists. A network created and referenced in
the same apply resolves once the network exists.

User groups, RADIUS profiles and firewall address and port groups are read for their
names only and have no directory: they are managed in the UI.

A zone's `network_ids` is neither dumped nor accepted: it is the controller's view of
which networks carry the zone's `firewall_zone_id`. A network is moved between zones in
its own file. A zone file is its name, and that is what creates the zone.

### Firewall policy order

The controller gives every zone pair a built-in policy of the same name, so a policy's
identity is its name and its two zones, as in `Allow All Traffic (Internal to External)`,
in the plan and in the file name alike. A file without both zones has no identity.

A policy's place in the order is its `index`, and the controller does not take it from
the policy: on create it numbers the policy itself, after the pair's others, and on update
it keeps the number the policy has. The order is set through one call per zone pair,
`firewall-policies/batch-reorder`, which lists the pair's custom policies before and
after the built-in ones, and the controller numbers them from 10000 in that order. So
`index` in a file is a rank: the numbers in a pair's files run from 10000 with no gap, as
the controller will number them.

`apply` sends every policy without its index, then one reorder for each pair in which a
file moved a policy, with the pair's policies in file order and the ones no file moves
kept in their place, and reads the numbers back. A policy numbered other than its file
says is reported as a kept change, like any other. What a built-in policy lets a file
change is for the controller to say, and the plan shows its answer.

### Shutting a port

The controller has no single key for a shut port. A port is shut by port security with no
address allowed, and the entry in `port_overrides` that does it carries seven keys:
`forward: disabled`, `native_networkconf_id: ""`, `port_security_enabled: true`,
`port_security_mac_address: []`, `tagged_vlan_mgmt: block_all`, `setting_preference:
auto` and `stp_edge_state: auto`. Four of them make the difference; the other three the
UI writes on every port it touches. `forward: disabled` on its own is accepted and
ignored: the controller answers ok and keeps `all`. With the network cleared as well it
stores `customize`. With the full entry it reports `forward` as `disabled`. A gateway
port and a switch port take the same entry.

## Plan

`plan` fetches every managed collection, matches each file to its document by identity,
and prints every key that differs, with both values, the controller's on the left. A file
with no document is a create. A document with no file is listed as unmanaged and left
alone: the tool never deletes anything on the console. The exit status is 2 when
something would change.

## Apply

`apply` runs `plan` first, then writes in dependency order: zones and AP groups,
networks, WLANs, port forwards, policies, settings, devices. An update sends the
controller's current document with the file's keys laid over it, so nothing the file
leaves out is lost.

`apply` then reads the document the controller answers with. The controller says ok and
keeps its own value when it will not take a change; `apply` names every key it kept and
exits 1, rather than report success over a plan that would show the same change again. A
setting the console has never stored is created through the same `set/setting/<key>`. A
device is never created: adoption is done on the console.

## One directory at a time

Invocations on one directory take turns: `dump` and `apply` hold its lock, `plan` shares
it, so two plans run together and a plan waits for an apply. Two applies at once would
plan from the same snapshot and both create the same documents; in turn, the second
finds the first's work done and has nothing to do. The lock is advisory and lives on the
machine running the tool: two machines against one console are not ordered.

## Not covered

Firewall address and port groups, traffic rules, DNS records and the client list have no
collection here and are managed in the UI. Adding one means reading what the controller
returns for it and declaring the kind: its endpoint, its identity key, and the keys that
are references.

## Running it

From a release, `unifictl` is on the path (the README covers installing and verifying
one). From a checkout, `just unifictl <args>` builds it into `.build/unifictl` and runs
it.

```bash
export UNIFI_URL=https://192.168.1.1 # the console; the factory address is the default
export UNIFI_API_KEY=...            # Control Plane → Integrations, on the console
export UNIFI_TLS_FINGERPRINT="$(openssl s_client -connect 192.168.1.1:443 </dev/null 2>/dev/null \
  | openssl x509 -noout -fingerprint -sha256 | cut -d= -f2)"
unifictl dump console               # once, to seed the files
unifictl plan console
unifictl apply console
```

The fingerprint is read off the console itself, on its LAN, once; a changed certificate
afterwards is a refused connection, not a silent acceptance.

`just test` runs the tests against a fake controller that answers the endpoints above
with an invented configuration. Nothing in the tests or in CI reaches a real console.
