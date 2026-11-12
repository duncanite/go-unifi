package consoletest

import "encoding/json"

// Ids the house documents carry, for tests that look them up.
const (
	ServicesID = "5fdecea2e8d72903cb0add01"
	ThingsID   = "5fdecea2e8d72903cb0add02"
	GroupID    = "5fdecea2e8d72903cb0add10"
	UserGroup  = "5fdecea2e8d72903cb0add20"
	ZoneID     = "5fdecea2e8d72903cb0add30"
	SwitchID   = "5fdecea2e8d72903cb0add40"
	WLANID     = "5fdecea2e8d72903cb0add50"
	ForwardID  = "5fdecea2e8d72903cb0add60"
	SettingID  = "5fdecea2e8d72903cb0add70"
	MDNSID     = "5fdecea2e8d72903cb0add71"
	RadiusID   = "5fdecea2e8d72903cb0add90"
	ExternalID = "5fdecea2e8d72903cb0add31"
	PolicyID   = "5fdecea2e8d72903cb0adda0"
	CamerasID  = "5fdecea2e8d72903cb0addb0"
	APID       = "5fdecea2e8d72903cb0add80"
	LabZoneID  = "5fdecea2e8d72903cb0add32"
)

// house is a small console as its API returns it: two networks, one WLAN
// with a secret, one port forward, the settings a console has (one to
// manage, one with a token and a last alert, one of ids, one generated, one
// measured, one empty), one switch with runtime state, one AP whose radios
// report their capabilities, the built-in zones and one the house made, and
// the lookup collections the references point at.
const house = `{
  "networkconf": [
    {"_id": "` + ServicesID + `", "site_id": "site", "attr_no_delete": true,
     "name": "Services", "purpose": "corporate", "vlan_enabled": false,
     "ip_subnet": "10.0.0.1/24", "dhcpd_leasetime": 86400, "firewall_zone_id": "` + ZoneID + `",
     "dhcpd_dns_enabled": true, "dhcpd_dns_1": "10.0.0.5"},
    {"_id": "` + ThingsID + `", "site_id": "site",
     "name": "Things", "purpose": "corporate", "vlan_enabled": true, "vlan": 30,
     "ip_subnet": "10.0.30.1/24", "dhcpd_leasetime": 86400, "firewall_zone_id": "` + ZoneID + `"}
  ],
  "wlanconf": [
    {"_id": "` + WLANID + `", "site_id": "site",
     "name": "backyard", "enabled": true, "security": "wpapsk",
     "networkconf_id": "` + ServicesID + `", "usergroup_id": "` + UserGroup + `", "ap_group_ids": ["` + GroupID + `"],
     "wlan_bands": ["2g", "5g"], "group_rekey": 3600,
     "x_passphrase": "not-the-real-one"}
  ],
  "portforward": [
    {"_id": "` + ForwardID + `", "site_id": "site",
     "name": "ssh", "enabled": true, "proto": "tcp", "src": "any",
     "dst_port": "22", "fwd": "10.0.0.10", "fwd_port": "22", "pfwd_interface": "wan"}
  ],
  "setting": [
    {"_id": "` + SettingID + `", "site_id": "site", "key": "mgmt",
     "auto_upgrade": true, "auto_upgrade_hour": 5, "led_enabled": false},
    {"_id": "5fdecea2e8d72903cb0add72", "site_id": "site", "key": "ips",
     "enabled_categories": [], "enabled_networks": ["` + ThingsID + `"], "last_alert_id": "", "utm_token": "not-a-real-token"},
    {"_id": "` + MDNSID + `", "site_id": "site", "key": "mdns",
     "enabled_for": "some", "enabled_for_network_ids": ["` + ServicesID + `"]},
    {"_id": "5fdecea2e8d72903cb0add73", "site_id": "site", "key": "peer_to_peer",
     "psk": "not-a-real-psk", "ssid": "generated"},
    {"_id": "5fdecea2e8d72903cb0add74", "site_id": "site", "key": "provider_capabilities",
     "download": 50075, "upload": 5927},
    {"_id": "5fdecea2e8d72903cb0add75", "site_id": "site", "key": "openvpn"},
    {"_id": "5fdecea2e8d72903cb0add76", "site_id": "site", "key": "magic_site_to_site_vpn",
     "enabled": true, "public_key": "not-a-real-key", "x_private_key": "not-a-real-key-either"},
    {"_id": "5fdecea2e8d72903cb0add77", "site_id": "site", "key": "global_switch",
     "radiusprofile_id": "` + RadiusID + `", "dot1x_fallback_networkconf_id": ""},
    {"_id": "5fdecea2e8d72903cb0add78", "site_id": "site", "key": "ether_lighting",
     "network_defaults": [{"key": "` + ServicesID + `", "raw_color_hex": "0544ff"}, {"key": "none", "raw_color_hex": "ffffff"}]}
  ],
  "device": [
    {"_id": "` + SwitchID + `", "site_id": "site", "mac": "02:00:5e:00:53:91", "name": "Pantry Switch",
     "type": "usw", "model": "US24P250", "uptime": 123456, "state": 1,
     "led_override": "off",
     "port_overrides": [
       {"port_idx": 1, "name": "AP", "native_networkconf_id": "` + ServicesID + `", "poe_mode": "auto"},
       {"port_idx": 12, "name": "Sizzling", "native_networkconf_id": "` + ThingsID + `", "forward": "native"}
     ],
     "port_table": [{"port_idx": 1, "up": true}]},
    {"_id": "` + APID + `", "site_id": "site", "mac": "02:00:5e:00:53:ec", "name": "Attic",
     "type": "uap", "model": "U7NHD", "uptime": 42, "state": 1,
     "led_override": "off", "bandsteering_mode": "off", "port_overrides": null,
     "radio_table": [
       {"name": "ra0", "radio": "ng", "channel": "11", "ht": 20, "tx_power_mode": "auto",
        "radio_caps": 147476, "max_txpower": 23, "min_txpower": 6, "nss": 2, "builtin_antenna": true}
     ]}
  ],
  "usergroup": [
    {"_id": "` + UserGroup + `", "site_id": "site", "name": "Default", "attr_no_delete": true}
  ],
  "radiusprofile": [
    {"_id": "` + RadiusID + `", "site_id": "site", "name": "Default", "attr_no_delete": true}
  ],
  "apgroups": [
    {"_id": "` + GroupID + `", "name": "All APs", "attr_no_delete": true, "device_macs": []}
  ],
  "firewall/zone": [
    {"_id": "` + ZoneID + `", "name": "Internal", "default_zone": true, "zone_key": "internal",
     "network_ids": ["` + ServicesID + `", "` + ThingsID + `"]},
    {"_id": "` + ExternalID + `", "name": "External", "default_zone": true, "zone_key": "external", "network_ids": []},
    {"_id": "` + LabZoneID + `", "name": "Lab", "default_zone": false, "network_ids": []}
  ],
  "firewall-policies": [
    {"_id": "5fdecea2e8d72903cb0adda1", "name": "Allow All Traffic", "predefined": true, "hits": 42, "last_hit": 1794297782908,
     "origin_id": "6af170535d69fafcfe098606",
     "action": "ALLOW", "enabled": true, "index": 10000, "protocol": "all", "ip_version": "BOTH", "logging": false,
     "source": {"zone_id": "` + ZoneID + `", "matching_target": "ANY", "match_opposite_networks": false},
     "destination": {"zone_id": "` + ExternalID + `", "matching_target": "ANY"}},
    {"_id": "5fdecea2e8d72903cb0adda2", "name": "Allow All Traffic", "predefined": true,
     "action": "ALLOW", "enabled": true, "index": 30000, "protocol": "all", "ip_version": "BOTH", "logging": false,
     "source": {"zone_id": "` + ZoneID + `", "matching_target": "ANY"},
     "destination": {"zone_id": "` + ZoneID + `", "matching_target": "ANY"}},
    {"_id": "5fdecea2e8d72903cb0adda3", "name": "Allow Port Forward ssh", "predefined": true, "origin_type": "port_forward",
     "origin_id": "` + ForwardID + `", "action": "ALLOW", "enabled": true, "index": 30000, "protocol": "tcp",
     "source": {"zone_id": "` + ExternalID + `", "matching_target": "ANY"},
     "destination": {"zone_id": "` + ZoneID + `", "matching_target": "IP", "ips": ["10.0.0.10"], "port": "22"}},
    {"_id": "` + PolicyID + `", "name": "Block Things to Services", "predefined": false,
     "action": "BLOCK", "enabled": true, "index": 10000, "protocol": "all", "ip_version": "IPV4", "logging": true,
     "source": {"zone_id": "` + ZoneID + `", "matching_target": "NETWORK", "network_ids": ["` + ThingsID + `"]},
     "destination": {"zone_id": "` + ZoneID + `", "matching_target": "NETWORK", "network_ids": ["` + ServicesID + `"]}}
  ],
  "firewallgroup": [
    {"_id": "` + CamerasID + `", "site_id": "site", "name": "Cameras", "group_type": "address-group", "group_members": ["10.0.2.50"]}
  ]
}`

// House returns the house documents, collection by collection.
func House() map[string][]map[string]any {
	var docs map[string][]map[string]any
	if err := json.Unmarshal([]byte(house), &docs); err != nil {
		panic(err)
	}

	return docs
}
