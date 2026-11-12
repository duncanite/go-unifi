package console

import (
	"slices"
	"strings"
)

// The kinds, by the controller's collection names.
const (
	NetworkConf    = "networkconf"
	WLANConf       = "wlanconf"
	PortForward    = "portforward"
	Setting        = "setting"
	Device         = "device"
	UserGroup      = "usergroup"
	RadiusProfile  = "radiusprofile"
	APGroup        = "apgroup"
	FirewallZone   = "firewallzone"
	FirewallPolicy = "firewallpolicy"
	FirewallGroup  = "firewallgroup"
)

// idField is the controller's id on every v1 document, identityName the
// key most kinds are named by.
const (
	idField      = "_id"
	identityName = "name"
	// dot1xFallback names the network an 802.1X failure lands on, on a
	// switch and in the global switch setting alike.
	dot1xFallback = "dot1x_fallback_networkconf_id"
)

// Kind is one collection of documents and the rules its documents follow:
// which key names a document, which keys are references to other kinds'
// documents, and which list-valued keys the controller keys entry by entry.
type Kind struct {
	// Name is the collection's name as the controller spells it, and the
	// directory the files live in.
	Name string
	// Identity is the key that names a document: name, key, or mac.
	Identity string
	// Qualifiers are the reference paths that tell two documents of one
	// name apart, where the controller repeats names: a policy is its name
	// and its two zones, "Allow All Traffic (Internal to External)".
	Qualifiers []string
	// Keys, when set, are the only keys that are the document's
	// configuration; the rest is runtime state the controller reports.
	Keys []string
	// Lists maps a list-valued key to the key that identifies its entries,
	// so entries merge one by one instead of the list as a whole.
	Lists map[string]string
	// Refs maps a key holding an id, or a list of ids, to the kind it points
	// at; a key inside a merged list is written list.key.
	Refs map[string]string
	// Own are the identities of documents that are the console's own: a
	// pairing it generated, a measurement it took. Neither dumped nor
	// accepted from a file.
	Own []string
	// OwnKey is a key whose presence marks a document as the console's own
	// the same way: the built-in zones carry zone_key.
	OwnKey string
	// Mirrors are keys whose value the controller derives from other
	// documents: a zone's network_ids is its networks' firewall_zone_id.
	// Dropped on dump, refused in a file; the other document states it.
	Mirrors []string
	// Sparse marks a kind whose document that has only its identity is
	// nothing: an empty setting. Such a document is neither dumped nor
	// listed; for any other kind a bare name is a document.
	Sparse bool
	// DerivedBy is a key whose presence marks a document the controller
	// derives from another: a policy it writes for a port forward or an
	// isolated network. Such a document is that other document's, neither
	// dumped nor accepted from a file.
	DerivedBy string
	// Rank is set on a kind the controller orders itself: a policy's index
	// is not taken from the policy, on create or update, but set through
	// one batch per zone pair.
	Rank *Ranking

	list   string
	create string
	update string
	idKey  string
	v2     bool
}

// Ranking is how the controller orders a kind's documents when it will not
// take the order from the document itself. Key holds the rank, and the
// controller numbers a group's documents itself, from 10000 in the order
// given, so a file states that number. Group maps the batch's keys to the
// paths whose values name a group, a policy's two zones. The documents
// marked by Own are ranked by the controller alone; the batch lists the
// others, those before the controller's own and those after.
type Ranking struct {
	Key   string
	Group map[string]string
	Own   string

	path   string
	before string
	after  string
}

// Managed reports whether the kind is written by apply; the lookup kinds
// exist for the references only.
func (k Kind) Managed() bool {
	return k.update != ""
}

// IsOwn reports whether the document is the console's own, by its identity
// or by the key that marks one.
func (k Kind) IsOwn(doc Document) bool {
	if k.OwnKey != "" {
		if _, own := doc[k.OwnKey]; own {
			return true
		}
	}

	identity, ok := doc[k.Identity].(string)

	return ok && slices.Contains(k.Own, identity)
}

// Mirrored reports whether key is one the controller derives from other
// documents on this kind.
func (k Kind) Mirrored(key string) bool {
	return slices.Contains(k.Mirrors, key)
}

// Derived reports whether the document is one the controller derives from
// another document.
func (k Kind) Derived(doc Document) bool {
	if k.DerivedBy == "" {
		return false
	}

	_, derived := doc[k.DerivedBy]

	return derived
}

// CreatesByUpdate reports whether a document of the kind that does not
// exist yet is created by writing it where an update goes: a setting is
// stored the first time set/setting/<key> is called.
func (k Kind) CreatesByUpdate() bool {
	return k.create == "" && k.idKey == k.Identity
}

// ID returns what Update needs as the document's id: the controller's _id,
// or the setting key for settings.
func (k Kind) ID(doc Document) string {
	id, ok := doc[k.idKey].(string)
	if !ok {
		return ""
	}

	return id
}

// Kinds returns every kind, the managed ones first in the order apply writes
// them: what is referenced comes before what references it, zones and AP
// groups before networks and WLANs, policies after both.
func Kinds() []Kind {
	return []Kind{
		// A zone's networks are the networks' zone, stated on each network
		// as firewall_zone_id; the built-in zones carry zone_key.
		mirrored(own(v2("firewall/zone", FirewallZone, nil), "zone_key"), "network_ids"),
		v2("apgroups", APGroup, nil),
		rest(NetworkConf, map[string]string{"firewall_zone_id": FirewallZone}),
		rest(WLANConf, map[string]string{
			"networkconf_id":   NetworkConf,
			"usergroup_id":     UserGroup,
			"ap_group_ids":     APGroup,
			"radiusprofile_id": RadiusProfile,
		}),
		rest(PortForward, nil),
		// A policy names its zones, networks and address or port groups
		// inside its source and destination.
		ranked(qualified(v2("firewall-policies", FirewallPolicy, map[string]string{
			"source.zone_id":            FirewallZone,
			"destination.zone_id":       FirewallZone,
			"source.network_ids":        NetworkConf,
			"destination.network_ids":   NetworkConf,
			"source.ip_group_id":        FirewallGroup,
			"destination.ip_group_id":   FirewallGroup,
			"source.port_group_id":      FirewallGroup,
			"destination.port_group_id": FirewallGroup,
		}), "source.zone_id", "destination.zone_id")),
		{
			Name: Setting, Identity: "key",
			// Every key a setting points at a network with, as the house
			// console has them, and the RADIUS profile two settings name.
			Refs: map[string]string{
				"enabled_for_network_ids":                 NetworkConf,
				"enabled_networks":                        NetworkConf,
				"excluded_network_ids":                    NetworkConf,
				"network_ids":                             NetworkConf,
				"fastleave_for_network_ids":               NetworkConf,
				"flood_unknown_multicast_for_network_ids": NetworkConf,
				dot1xFallback:                             NetworkConf,
				"network_defaults.key":                    NetworkConf,
				"radiusprofile_id":                        RadiusProfile,
			},
			// peer_to_peer is the pairing the console generated, a PSK and
			// an SSID; provider_capabilities is the last speed test.
			Own:    []string{"peer_to_peer", "provider_capabilities"},
			Sparse: true,
			list:   "rest/setting",
			update: "set/setting",
			idKey:  "key",
		},
		{
			Name: Device, Identity: "mac",
			Keys:  deviceKeys(),
			Lists: map[string]string{"port_overrides": "port_idx", "radio_table": identityName},
			Refs: map[string]string{
				"mgmt_network_id":                      NetworkConf,
				dot1xFallback:                          NetworkConf,
				"port_overrides.native_networkconf_id": NetworkConf,
				"port_overrides.voice_networkconf_id":  NetworkConf,
			},
			list:   "stat/device",
			update: "rest/device",
			idKey:  idField,
		},
		{Name: UserGroup, Identity: identityName, list: "rest/usergroup"},
		{Name: RadiusProfile, Identity: identityName, list: "rest/radiusprofile"},
		{Name: FirewallGroup, Identity: identityName, list: "rest/firewallgroup"},
	}
}

// qualified is kind with the paths that tell its documents of one name
// apart, and, for a policy, the key that marks one derived from a port
// forward or an isolated network.
func qualified(kind Kind, paths ...string) Kind {
	kind.Qualifiers = paths
	kind.DerivedBy = "origin_type"

	return kind
}

// ranked marks a policy kind as ordered by the controller: its index is
// set through firewall-policies/batch-reorder, one batch per zone pair,
// which lists the pair's user policies before and after the predefined.
func ranked(kind Kind) Kind {
	kind.Rank = &Ranking{
		Key:    "index",
		Group:  map[string]string{"source_zone_id": "source.zone_id", "destination_zone_id": "destination.zone_id"},
		Own:    "predefined",
		path:   kind.update + "/batch-reorder",
		before: "before_predefined_ids",
		after:  "after_predefined_ids",
	}

	return kind
}

// own marks kind's documents that carry key as the console's own.
func own(kind Kind, key string) Kind {
	kind.OwnKey = key

	return kind
}

// mirrored marks keys of kind the controller derives from other documents.
func mirrored(kind Kind, keys ...string) Kind {
	kind.Mirrors = keys

	return kind
}

// v2 is a kind the controller keeps under its v2 API at path, named by
// name and created, listed and updated there; the answers are plain.
func v2(path, name string, refs map[string]string) Kind {
	return Kind{
		Name: name, Identity: identityName, Refs: refs,
		list:   path,
		create: path,
		update: path,
		idKey:  idField,
		v2:     true,
	}
}

// rest is a kind the controller keeps under rest/<name>, named by name and
// created, listed and updated there.
func rest(name string, refs map[string]string) Kind {
	return Kind{
		Name: name, Identity: identityName, Refs: refs,
		list:   "rest/" + name,
		create: "rest/" + name,
		update: "rest/" + name,
		idKey:  idField,
	}
}

// deviceKeys are the keys rest/device takes: what the UI sets on a device,
// as opposed to what stat/device reports about it.
func deviceKeys() []string {
	return []string{
		"mac", "name",
		"led_override", "led_override_color", "led_override_color_brightness",
		"lcm_brightness", "lcm_brightness_override", "lcm_idle_timeout", "lcm_idle_timeout_override",
		"lcm_night_mode_enabled", "lcm_night_mode_begins", "lcm_night_mode_ends",
		"port_overrides", "radio_table", "ethernet_overrides", "wlan_overrides",
		"mgmt_network_id", "config_network",
		"snmp_contact", "snmp_location",
		"outdoor_mode_override", "bandsteering_mode", "atf_enabled",
		"jumboframe_enabled", "flowctrl_enabled", "stp_priority", "stp_version", "switch_vlan_enabled",
		"dot1x_portctrl_enabled", dot1xFallback,
		"power_source_ctrl_enabled", "lowpowermode_enabled",
		"disabled", "locked",
	}
}

// Owned reports whether the controller owns key: an id, a site, a flag it
// sets on built-in documents, what it reports rather than takes, or a
// secret. None of them belongs in a file, at the top or inside an entry.
func Owned(key string) bool {
	switch key {
	case idField, "site_id", "external_id", "short_id",
		// The public half of the site-to-site VPN key the console made;
		// the private half is x_private_key. Neither is a file's to set.
		"public_key":
		return true
	}

	return strings.HasPrefix(key, "attr_") || Reported(key) || Secret(key)
}

// Reported reports whether key is state the controller writes into a
// configuration document: a radio's hardware capabilities, the last alert
// it saw, the release it last asked about analytics on.
func Reported(key string) bool {
	switch key {
	case "last_alert_id", "analytics_disapproved_for",
		// A zone's built-in key and flag, a policy's built-in flag, the
		// counters on a policy, and the console's own cross-references.
		"zone_key", "default_zone", "predefined",
		"hits", "last_hit", "origin_id", "cloud_template",
		"radio_caps", "radio_caps2", "max_txpower", "min_txpower",
		"builtin_ant_gain", "builtin_antenna", "current_antenna_gain",
		"has_dfs", "has_fccdfs", "has_ht160", "is_11ac", "nss":
		return true
	}

	return false
}

// Secret reports whether key is a secret: one the controller hides behind
// x_, a passphrase, an authentication key, and the few it leaves in the
// clear: the peer_to_peer PSK, the per-client PSK lists, the threat
// management token.
func Secret(key string) bool {
	switch key {
	case "psk", "sae_psk", "private_preshared_keys", "utm_token":
		return true
	}

	return strings.HasPrefix(key, "x_")
}
