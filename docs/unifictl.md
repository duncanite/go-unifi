# unifictl

The console as files, with a tool of our own between them: `unifictl` reads the
controller's own documents, writes them out as YAML, and makes the controller match the
files again. It replaced the Terraform provider the console it was written against was
first brought under, once its plan was quiet against the same console; that console's own
files and notes live with its owner, in a repository of their own, and this one holds the
tool.

## Why not the provider

The provider translates the controller's documents into a schema of its own, and the
translation is where every problem came from: fields the controller takes that the
schema lacks (shutting a port, the WAN MAC override, https://github.com/duncanite/infra/issues/4 and https://github.com/duncanite/infra/issues/6), fields
the schema spells differently from the controller, defaults the provider invents, and a
plan that cannot say whether a value is stated or merely observed. Every fix meant a
round trip through two upstream repositories. The reconciler has no translation layer:
a file holds the controller's keys with the controller's spellings, and what the
controller accepts, the file can say.

## Shape

The same shape as forkcloser/infra's `dnsctl`: a kong command line, primordium for the
process, YAML files, and a reconciler that reads both sides and prints every difference
before it changes anything.

```
unifictl dump  console/   # write every managed document the console holds
unifictl plan  console/   # print what apply would change; exit 2 when something would
unifictl apply console/   # make the console match the files
```

The console's address, site and API key come from the environment (`UNIFI_URL`,
`UNIFI_SITE`, `UNIFI_API_KEY`), never from the tree. The console's certificate is
self-signed; `UNIFI_TLS_FINGERPRINT` pins it by its SHA-256, and `--insecure` accepts
anything, for a console whose certificate is about to change. Nothing runs in CI: the
console is on the house LAN.

## Documents

A directory per collection, named as the controller names it, one YAML file per
document, named after the document's identity:

| collection    | identity | the controller's endpoint                                  |
| ------------- | -------- | ---------------------------------------------------------- |
| `networkconf` | `name`   | `rest/networkconf`                                         |
| `wlanconf`    | `name`   | `rest/wlanconf`                                            |
| `portforward` | `name`   | `rest/portforward`                                         |
| `setting`     | `key`    | `rest/setting`, written through `set/setting/<key>`        |
| `device`      | `mac`    | `stat/device`, written through `rest/device/<_id>`         |
| `firewallzone` | `name`  | `v2/.../firewall/zone`, a plain array; written at `/<_id>`; a zone is its name, its networks are the networks' |
| `firewallpolicy` | `name` | `v2/.../firewall-policies`, the same shape                |
| `apgroup`     | `name`   | `v2/.../apgroups`, the same shape                          |

A file is a partial document: the keys it states are enforced, the keys it leaves out
are the controller's to keep, and a map inside it is partial the same way, key by key,
down through maps: a policy's `source` may state its zone and networks and leave the
flags the controller adds to the controller. `dump` writes every key the controller holds, so a dumped
file is complete and recreates its object as it was; pruning it to what matters is a
choice made file by file, in the open, and so are comments: `dump` carries a file's
comments onto the document that replaces it, a key's with the key, a port's with the
port, a block set apart at the top and the file's closing line where they were, and names the one whose key the console no
longer has. Pruning does not survive a forced dump; comments do. `dump` makes each
directory hold the console's documents and no other: a file for a document since
renamed or deleted on the console is removed, so it cannot come back as a create. A file that is not a document (a `.yml`, a
stray extension) is refused by `plan` and `apply`, not skipped, and left where it is by `dump`. A list the controller keys by entry
(`port_overrides` by `port_idx`, `radio_table` by `name`) is merged entry by entry under
the same rule: an entry in the file is enforced key by key, an entry absent from the
file is left as it is, and an entry without its key is refused.

Three kinds of key never appear in a file, at the top or anywhere below it, inside a
list's entry or a nested map:

- The controller's own: `_id`, `site_id`, `external_id`, `short_id`, the `attr_*`
  flags, `public_key` (the site-to-site VPN key it made; the private half is `x_`), and
  what it reports into a configuration document rather than takes from it:
  a radio's capabilities (`radio_caps`, `max_txpower`, `nss`, ...), `last_alert_id`,
  `analytics_disapproved_for`, a policy's `hits` and `last_hit` and the `origin_id` and
  `cloud_template` the console cross-references with. `dump` drops them; `plan` refuses a
  file that states one.
- Secrets: every `x_*` key (WLAN passphrases, authentication keys) and the few the
  controller leaves in the clear: `psk`, `sae_psk`, `private_preshared_keys`,
  `utm_token`. `dump` drops them, `plan` refuses them, and `apply` sends the controller
  its own current values back, so a passphrase set once in the UI survives every apply. A
  WLAN created from a file has no passphrase until one is set; that gap is deliberate
  for now.
- A device's runtime state. `stat/device` returns the device as it is, uptime and
  counters included; the device kind keeps the configuration keys only.

Three kinds of document are no file at all, and are not listed as unmanaged either: the
console's own, generated or measured there (`peer_to_peer`, the pairing it made itself,
and `provider_capabilities`, its last speed test); a document the controller derives from
another, the policy it writes for each port forward and for an isolated network, marked
by `origin_type`, which is that other document's to change; and a setting that is its
key and nothing else, whose file would enforce nothing. The built-in zones, marked by
`zone_key`, are the console's own the same way: nothing on them is a file's to set.
`plan` refuses a file for the first two.

References carry names, not ids. Every key that points at a network (`networkconf_id`,
`native_networkconf_id`, `voice_networkconf_id`, `mgmt_network_id`,
`dot1x_fallback_networkconf_id`, the settings' `*_network_ids` lists and the LED colour
list's keys), at an AP group (`ap_group_ids`), a user group (`usergroup_id`), a RADIUS
profile (`radiusprofile_id`) or a firewall zone (`firewall_zone_id`) is written by `dump`
as the name of the object it points at, and `plan` resolves the name back to the id,
failing loudly on a name the console does not know. A zone's `network_ids` is the
controller's view of its networks' `firewall_zone_id`, neither dumped nor accepted: a
network is moved by its own file, and a zone file is a name, which is what creates one. The controller's own words at a
reference, the empty string on a port with no network or `none` in the LED list, are
learnt from what it holds and pass as they are. So does a raw id, 24 hex digits: `plan`
sends it as it is and `dump` writes one where it has no name for what is pointed at,
so a file carrying an id names something the console no longer lists. A network created and referenced in the
same apply resolves after the network exists. The
collections read for names alone (user groups, RADIUS profiles, firewall address and port
groups) have no directory: they are the UI's. A policy's `source` and `destination` name
their zone, networks and groups the same way, through the map.

The controller gives every zone pair a built-in policy of the same name, so a policy is
named by its name and its two zones, `Allow All Traffic (Internal to External)`, in the
plan and in the file name alike; a file without both zones has no identity. A policy's
place in the order is its `index`, and the controller does not take it from the policy:
on create it numbers the policy itself, after the pair's others, and on update it keeps
the number it has. The order is set through one batch per zone pair,
`firewall-policies/batch-reorder`, which lists the pair's policies that are not the
controller's own, those before the built-in ones and those after, and the controller
numbers them from 10000 in that order. So `index` in a file is a rank, and the numbers in
a pair's files run from 10000 with no gap, as the controller will number them; `apply`
sends every policy without its index, then one batch for each pair a file moved a policy
in, with the pair's policies in file order and the ones no file moves kept in their place,
and reads the numbers back: a policy numbered other than its file says is a kept change,
like any other. The built-in policies carry `predefined`, the controller's own, dropped
like `_id`; what a built-in policy lets a file change is for the controller to say, and the
plan shows its answer.

One recipe the controller does not spell out, read off a port shut in the UI and then
dumped: a port is shut by port security with no address allowed. The entry carries
`forward: disabled`, `native_networkconf_id: ""`, `port_security_enabled: true`,
`port_security_mac_address: []`, `tagged_vlan_mgmt: block_all`, `setting_preference:
auto` and `stp_edge_state: auto`; the controller then reports `forward` as `disabled`.
With `forward: disabled` alone it answers ok and keeps `all`, which is what the
provider ran into (https://github.com/duncanite/infra/issues/6); with the network cleared too, it stores
`customize`. Four keys of those are the difference, the rest the UI fills on every port
it touches; a gateway port and a switch port take the same.

## Plan and apply

`plan` fetches every managed collection, matches each file to its document by identity,
and prints every differing key with both values, the controller's on the left. A file
with no document is a create; a document with no file is listed as unmanaged and left
alone: the tool deletes nothing. Exit 2 says something would change, as
`tofu plan -detailed-exitcode` does.

`apply` runs `plan` first, then writes in dependency order: zones and AP groups, networks,
WLANs, port forwards, policies, settings, devices. An update sends the controller's current
document with the file's keys laid over it, so nothing the file leaves out is lost, and
reads the document the controller answers with: the controller says ok and keeps its own
values when it will not take a change, and `apply` then names every key it kept and exits
1 rather than print Applied over a plan that will show the same change again. A setting the console
has never stored is created by setting it, through the same `set/setting/<key>`. A
device is never created: adoption is the console's.

Invocations on one directory take turns: `dump` and `apply` hold its lock, `plan` shares
it, so two plans run together and a plan waits for an apply. Two applies at once would
plan from the same snapshot and both create the same documents; in turn, the second
finds the first's work and has nothing to do. The lock is advisory and the host's: two
machines against one console are not ordered, and are not how this is run.

## Milestones

All three landed on 2026-11-09 and 2026-11-10.

1. **Read-only.** `dump` and `plan` against the console, the files committed under
   `console/`, the plan quiet. The provider stayed; the two were compared on the same
   console.
2. **Apply.** Changes land through `unifictl apply`; the provider's tree went in its own
   pull request, the state file with it.
3. **The rest of the console.** Zone-based firewall zones and policies and the AP groups
   are kinds like the others, through the v2 endpoints. What is left to the UI is what
   has no collection here yet: firewall address and port groups, traffic rules, DNS
   records, the client list; each goes the same way when it is wanted, by reading the
   record first.

## Running it

```bash
export UNIFI_API_KEY=...            # Control Plane → Integrations, on the console
export UNIFI_TLS_FINGERPRINT="$(openssl s_client -connect 10.0.0.1:443 </dev/null 2>/dev/null \
  | openssl x509 -noout -fingerprint -sha256 | cut -d= -f2)"
just unifictl dump console          # once, to seed the files
just unifictl plan console
just unifictl apply console
```

The fingerprint is read off the console itself, on the LAN, the one time; a changed
certificate afterwards is a refused connection, not a silent acceptance.

The binary is built from this module at the commit checked out (`.build/unifictl`);
`just test` runs its tests against a fake controller, and nothing in CI reaches the
house.
