package reconcile_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/duncanite/go-unifi/console"
	"github.com/duncanite/go-unifi/console/consoletest"
	"github.com/duncanite/go-unifi/documents"
	"github.com/duncanite/go-unifi/reconcile"
)

func house(t *testing.T) (*consoletest.Server, *reconcile.Reconciler) {
	t.Helper()

	srv := consoletest.New(consoletest.House())
	t.Cleanup(srv.Close)

	client, err := console.New(srv.Client(), srv.URL, consoletest.APIKey, "")
	if err != nil {
		t.Fatal(err)
	}

	return srv, &reconcile.Reconciler{Console: client}
}

// reload writes the set out and reads it back, as dump then plan do.
func reload(t *testing.T, set *documents.Set) *documents.Set {
	t.Helper()

	dir := t.TempDir()

	for _, kind := range console.Kinds() {
		if docs, ok := set.Kinds[kind.Name]; ok {
			if _, err := documents.Write(dir, kind, docs); err != nil {
				t.Fatal(err)
			}
		}
	}

	loaded, err := documents.Load(dir, console.Kinds())
	if err != nil {
		t.Fatal(err)
	}

	return loaded
}

func asList(t *testing.T, value any) []any {
	t.Helper()

	list, ok := value.([]any)
	if !ok {
		t.Fatalf("%#v is not a list", value)
	}

	return list
}

func asMap(t *testing.T, value any) map[string]any {
	t.Helper()

	entry, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%#v is not a map", value)
	}

	return entry
}

func find(t *testing.T, set *documents.Set, kind, identity string) console.Document {
	t.Helper()

	for _, k := range console.Kinds() {
		if k.Name != kind {
			continue
		}

		for _, doc := range set.Kinds[kind] {
			if documents.Identity(k, doc) == identity {
				return doc
			}
		}
	}

	t.Fatalf("no %s %q in the set", kind, identity)

	return nil
}

// TestDumpKeepsTheConsolesOwnToItself: the pairing the console generated,
// the speed test it measured and the empty setting are no files; a token,
// a last alert and a radio's capabilities are not in the files that are.
func TestDumpKeepsTheConsolesOwnToItself(t *testing.T) {
	t.Parallel()

	_, rec := house(t)

	set, err := rec.Dump(t.Context())
	if err != nil {
		t.Fatalf("Dump: %v", err)
	}

	for _, doc := range set.Kinds["setting"] {
		switch doc["key"] {
		case "peer_to_peer", "provider_capabilities", "openvpn":
			t.Fatalf("setting %q dumped: %v", doc["key"], doc)
		}
	}

	ips := find(t, set, "setting", "ips")
	if _, secret := ips["utm_token"]; secret {
		t.Fatal("the threat management token was dumped")
	}

	if _, reported := ips["last_alert_id"]; reported {
		t.Fatal("the last alert was dumped")
	}

	if networks := asList(t, ips["enabled_networks"]); len(networks) != 1 || networks[0] != "Things" {
		t.Fatalf("enabled_networks = %v, want the network's name", ips["enabled_networks"])
	}

	vpn := find(t, set, "setting", "magic_site_to_site_vpn")
	if _, key := vpn["public_key"]; key || vpn["enabled"] != true {
		t.Fatalf("the VPN setting is its switch alone: %v", vpn)
	}

	mdns := find(t, set, "setting", "mdns")
	if networks := asList(t, mdns["enabled_for_network_ids"]); len(networks) != 1 || networks[0] != "Services" {
		t.Fatalf("enabled_for_network_ids = %v, want the network's name", mdns["enabled_for_network_ids"])
	}

	radios := asList(t, find(t, set, "device", "02:00:5e:00:53:ec")["radio_table"])
	if len(radios) != 1 {
		t.Fatalf("radio_table = %v", radios)
	}

	radio := asMap(t, radios[0])
	if radio["channel"] != "11" || radio["tx_power_mode"] != "auto" {
		t.Fatalf("the radio's configuration was pruned: %v", radio)
	}

	for _, key := range []string{"radio_caps", "max_txpower", "min_txpower", "nss", "builtin_antenna"} {
		if _, reported := radio[key]; reported {
			t.Fatalf("%s, a capability, was dumped: %v", key, radio)
		}
	}
}

// TestDumpNamesEveryReference: the RADIUS profile and every network a
// setting points at, including the LED colour list's keys, come out by
// name; the controller's words for no network stay as they are.
func TestDumpNamesEveryReference(t *testing.T) {
	t.Parallel()

	_, rec := house(t)

	set, err := rec.Dump(t.Context())
	if err != nil {
		t.Fatalf("Dump: %v", err)
	}

	sw := find(t, set, "setting", "global_switch")
	if sw["radiusprofile_id"] != "Default" || sw["dot1x_fallback_networkconf_id"] != "" {
		t.Fatalf("global_switch = %v, want the profile by name and the fallback empty", sw)
	}

	defaults := asList(t, find(t, set, "setting", "ether_lighting")["network_defaults"])
	if len(defaults) != 2 || asMap(t, defaults[0])["key"] != "Services" || asMap(t, defaults[1])["key"] != "none" {
		t.Fatalf("network_defaults = %v, want Services and none", defaults)
	}

	// The AP reports port_overrides as null: a reference path through a
	// list that is not there leaves it null, not an empty list.
	if ports, present := find(t, set, "device", "02:00:5e:00:53:ec")["port_overrides"]; !present || ports != nil {
		t.Fatalf("port_overrides = %#v, want null kept as null", ports)
	}
}

// TestFirewallAsFiles: zones, policies and AP groups dump by name, inside
// a policy's source and destination too, and go back as ids through the
// v2 endpoints, created and updated.
func TestFirewallAsFiles(t *testing.T) {
	t.Parallel()

	srv, rec := house(t)

	set, err := rec.Dump(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	// The built-in zones are the console's own; a zone the owner made is a
	// file, and its networks are not in it: each network states its zone.
	for _, doc := range set.Kinds["firewallzone"] {
		if doc["name"] == "External" || doc["name"] == "Internal" {
			t.Fatalf("a built-in zone was dumped: %v", doc)
		}
	}

	if lab := find(t, set, "firewallzone", "Lab"); len(lab) != 1 {
		t.Fatalf("a zone is its name and nothing else: %v", lab)
	}

	block := find(t, set, "firewallpolicy", "Block Things to Services (Internal to Internal)")
	if _, own := block["predefined"]; own {
		t.Fatal("predefined dumped on a policy")
	}

	// Two built-in policies share a name; their zones tell them apart. The
	// counters and the console's cross-reference are not in the file; the
	// policy the controller wrote for a port forward is no file at all.
	allow := find(t, set, "firewallpolicy", "Allow All Traffic (Internal to External)")
	for _, key := range []string{"hits", "last_hit", "origin_id"} {
		if _, own := allow[key]; own {
			t.Fatalf("%s dumped on a policy: %v", key, allow)
		}
	}

	find(t, set, "firewallpolicy", "Allow All Traffic (Internal to Internal)")

	for _, doc := range set.Kinds["firewallpolicy"] {
		if doc["name"] == "Allow Port Forward ssh" {
			t.Fatalf("a derived policy was dumped: %v", doc)
		}
	}

	source := asMap(t, block["source"])
	if source["zone_id"] != "Internal" || fmt.Sprint(asList(t, source["network_ids"])) != "[Things]" ||
		fmt.Sprint(block["index"]) != "10000" {
		t.Fatalf("policy not by name, or its place lost: %v", block)
	}

	set = reload(t, set)

	// One policy off and pointed at another network, its source stated in
	// part; one created with its zones and networks by name; one AP group
	// created.
	blockFile := find(t, set, "firewallpolicy", "Block Things to Services (Internal to Internal)")
	blockFile["enabled"] = false
	blockFile["source"] = map[string]any{"zone_id": "Internal", "network_ids": []any{"Services"}}

	set.Kinds["firewallpolicy"] = append(set.Kinds["firewallpolicy"], console.Document{
		"name":    "Allow Services to Things",
		"action":  "ALLOW",
		"enabled": true,
		"index":   int64(10001),
		"source": map[string]any{
			"zone_id":         "Internal",
			"matching_target": "NETWORK",
			"network_ids":     []any{"Services"},
		},
		"destination": map[string]any{
			"zone_id":         "Internal",
			"matching_target": "NETWORK",
			"network_ids":     []any{"Things"},
		},
	})
	set.Kinds["apgroup"] = append(set.Kinds["apgroup"], console.Document{"name": "Garden", "device_macs": []any{}})

	// A zone created and a network moved into it in the same apply: the
	// zone is written first, the network then names it.
	set.Kinds["firewallzone"] = append(set.Kinds["firewallzone"], console.Document{"name": "Garage"})
	find(t, set, "networkconf", "Things")["firewall_zone_id"] = "Garage"

	plan, err := rec.Plan(t.Context(), set)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer

	plan.Write(&out)

	for _, want := range []string{
		"firewallpolicy \"Block Things to Services (Internal to Internal)\"\n  ~ enabled: true -> false\n" +
			"  ~ source.network_ids: [\"Things\"] -> [\"Services\"]\n",
		"  + source: {\"matching_target\":\"NETWORK\",\"network_ids\":[\"Services\"],\"zone_id\":\"Internal\"}\n",
		"apgroup \"Garden\" (create)\n",
		"firewallzone \"Garage\" (create)\n",
		"networkconf \"Things\"\n  ~ firewall_zone_id: \"Internal\" -> \"Garage\"\n",
		"5 changes.\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("plan lacks %q:\n%s", want, out.String())
		}
	}

	if applyErr := rec.Apply(t.Context(), plan); applyErr != nil {
		t.Fatalf("Apply: %v", applyErr)
	}

	want := []string{
		"POST v2/api/site/default/firewall/zone",
		"POST v2/api/site/default/apgroups",
		"PUT api/s/default/rest/networkconf/" + consoletest.ThingsID,
		"POST v2/api/site/default/firewall-policies",
		"PUT v2/api/site/default/firewall-policies/" + consoletest.PolicyID,
		"PUT v2/api/site/default/firewall-policies/batch-reorder",
	}
	if strings.Join(srv.Requests, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(srv.Requests, "\n"), strings.Join(want, "\n"))
	}

	garage, ok := srv.Bodies[0]["_id"].(string)
	if !ok || garage == "" || srv.Bodies[2]["firewall_zone_id"] != garage {
		t.Fatalf("the network does not name the zone made just before it: %v / %v", srv.Bodies[0], srv.Bodies[2])
	}

	created := asMap(t, srv.Bodies[3]["source"])
	if created["zone_id"] != consoletest.ZoneID ||
		fmt.Sprint(asList(t, created["network_ids"])) != "["+consoletest.ServicesID+"]" {
		t.Fatalf("the policy went out by name: %v", srv.Bodies[3])
	}

	if _, sent := srv.Bodies[3]["index"]; sent {
		t.Fatalf("the rank went out with the create; the batch ranks: %v", srv.Bodies[3])
	}

	newID, ok := srv.Bodies[3]["_id"].(string)
	if !ok {
		t.Fatalf("the created policy has no id: %v", srv.Bodies[3])
	}

	batch := srv.Bodies[5]
	if batch["source_zone_id"] != consoletest.ZoneID || batch["destination_zone_id"] != consoletest.ZoneID ||
		fmt.Sprint(asList(t, batch["before_predefined_ids"])) != "["+consoletest.PolicyID+" "+newID+"]" ||
		fmt.Sprint(asList(t, batch["after_predefined_ids"])) != "[]" {
		t.Fatalf("the batch does not rank the pair's user policies in file order: %v", batch)
	}

	updated := srv.Bodies[4]
	updatedSource := asMap(t, updated["source"])

	if updated["enabled"] != false || updated["_id"] != consoletest.PolicyID ||
		updatedSource["zone_id"] != consoletest.ZoneID || updatedSource["matching_target"] != "NETWORK" ||
		fmt.Sprint(asList(t, updatedSource["network_ids"])) != "["+consoletest.ServicesID+"]" {
		t.Fatalf("the update did not carry the whole policy, its source merged key by key: %v", updated)
	}

	plan, err = rec.Plan(t.Context(), set)
	if err != nil {
		t.Fatal(err)
	}

	if !plan.Empty() {
		out.Reset()
		plan.Write(&out)
		t.Fatalf("plan after apply:\n%s", out.String())
	}
}

func TestDumpWritesFilesAsFilesShouldBe(t *testing.T) {
	t.Parallel()

	_, rec := house(t)

	set, err := rec.Dump(t.Context())
	if err != nil {
		t.Fatalf("Dump: %v", err)
	}

	if len(set.Kinds) != 8 {
		t.Fatalf("kinds = %v, want the eight managed ones", set.Kinds)
	}

	services := find(t, set, "networkconf", "Services")
	if _, owned := services["_id"]; owned {
		t.Fatal("_id dumped")
	}

	if _, owned := services["attr_no_delete"]; owned {
		t.Fatal("attr_no_delete dumped")
	}

	if services["firewall_zone_id"] != "Internal" {
		t.Fatalf("firewall_zone_id = %v, want the zone's name", services["firewall_zone_id"])
	}

	wlan := find(t, set, "wlanconf", "backyard")
	if _, secret := wlan["x_passphrase"]; secret {
		t.Fatal("the passphrase was dumped")
	}

	if wlan["networkconf_id"] != "Services" || wlan["usergroup_id"] != "Default" {
		t.Fatalf("references not by name: %v", wlan)
	}

	if groups := asList(t, wlan["ap_group_ids"]); len(groups) != 1 || groups[0] != "All APs" {
		t.Fatalf("ap_group_ids = %v, want the group's name", wlan["ap_group_ids"])
	}

	sw := find(t, set, "device", "02:00:5e:00:53:91")
	for _, runtime := range []string{"uptime", "state", "port_table", "type", "model"} {
		if _, present := sw[runtime]; present {
			t.Fatalf("runtime key %q dumped", runtime)
		}
	}

	ports := asList(t, sw["port_overrides"])
	if len(ports) != 2 {
		t.Fatalf("port_overrides = %v", sw["port_overrides"])
	}

	if entry := asMap(t, ports[1]); entry["native_networkconf_id"] != "Things" {
		t.Fatalf("port 12 native network = %v, want Things", entry)
	}
}

func TestPlanIsQuietAfterDump(t *testing.T) {
	t.Parallel()

	_, rec := house(t)

	set, err := rec.Dump(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	plan, err := rec.Plan(t.Context(), reload(t, set))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	var out bytes.Buffer

	plan.Write(&out)

	if !plan.Empty() || len(plan.Unmanaged) != 0 || out.String() != "No changes.\n" {
		t.Fatalf("plan after dump:\n%s\nunmanaged: %v", out.String(), plan.Unmanaged)
	}
}

func TestPlanSeesEveryKindOfChange(t *testing.T) {
	t.Parallel()

	_, rec := house(t)

	set, err := rec.Dump(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	set = reload(t, set)

	// A scalar on a network, a key inside a merged list entry, a new entry
	// in that list, a setting, and a new network with a new WLAN on it.
	find(t, set, "networkconf", "Services")["dhcpd_leasetime"] = int64(3600)

	ports := asList(t, find(t, set, "device", "02:00:5e:00:53:91")["port_overrides"])
	asMap(t, ports[1])["name"] = "Sizzling (Sonos)"

	find(t, set, "device", "02:00:5e:00:53:91")["port_overrides"] = append(ports,
		map[string]any{"port_idx": int64(16), "name": "Lutron bridge", "native_networkconf_id": "Things"})

	find(t, set, "setting", "mgmt")["led_enabled"] = true

	// A reference moved: shown by name on both sides.
	find(t, set, "wlanconf", "backyard")["networkconf_id"] = "Things"

	set.Kinds["networkconf"] = append(set.Kinds["networkconf"],
		console.Document{"name": "Guests", "purpose": "corporate", "vlan_enabled": true, "vlan": int64(40)})
	set.Kinds["wlanconf"] = append(
		set.Kinds["wlanconf"],
		console.Document{
			"name":           "les_amis",
			"networkconf_id": "Guests",
			"usergroup_id":   "Default",
			"ap_group_ids":   []any{"All APs"},
		},
	)
	set.Kinds["portforward"] = nil

	plan, err := rec.Plan(t.Context(), set)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	var out bytes.Buffer

	plan.Write(&out)

	for _, want := range []string{
		"networkconf \"Services\"\n  ~ dhcpd_leasetime: 86400 -> 3600\n",
		"  ~ port_overrides[12].name: \"Sizzling\" -> \"Sizzling (Sonos)\"\n",
		"  + port_overrides[16]: {\"name\":\"Lutron bridge\",\"native_networkconf_id\":\"Things\",\"port_idx\":16}\n",
		"setting \"mgmt\"\n  ~ led_enabled: false -> true\n",
		"wlanconf \"backyard\"\n  ~ networkconf_id: \"Services\" -> \"Things\"\n",
		"networkconf \"Guests\" (create)\n  + name: \"Guests\"\n",
		"wlanconf \"les_amis\" (create)\n",
		"  + ap_group_ids: [\"All APs\"]\n",
		"  + networkconf_id: \"Guests\"\n",
		"  + usergroup_id: \"Default\"\n",
		"unmanaged, left alone: portforward \"ssh\"\n",
		"6 changes.\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("plan lacks %q:\n%s", want, out.String())
		}
	}
}

func TestPlanRefusesAReferenceNobodyDeclares(t *testing.T) {
	t.Parallel()

	_, rec := house(t)

	set, err := rec.Dump(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	set = reload(t, set)
	find(t, set, "wlanconf", "backyard")["networkconf_id"] = "Peoples"

	_, err = rec.Plan(t.Context(), set)
	if !errors.Is(err, reconcile.ErrUnresolved) || !strings.Contains(err.Error(), `no networkconf named "Peoples"`) {
		t.Fatalf("err = %v, want the unresolved name", err)
	}

	find(t, set, "wlanconf", "backyard")["networkconf_id"] = "Services"
	set.Kinds["device"] = append(set.Kinds["device"], console.Document{"mac": "00:00:00:00:00:01", "name": "ghost"})

	_, err = rec.Plan(t.Context(), set)
	if !errors.Is(err, reconcile.ErrUnresolved) || !strings.Contains(err.Error(), "not adopted") {
		t.Fatalf("err = %v, want the unadopted device refused", err)
	}
}

// TestApplyRefusesWhatTheConsoleKept: the console answers ok to a setting
// and keeps its own value; apply reads the answer and says so, instead of
// printing Applied over a plan that will show the same change again.
func TestApplyRefusesWhatTheConsoleKept(t *testing.T) {
	t.Parallel()

	srv, rec := house(t)
	srv.Stubborn = map[string][]string{"setting": {"led_enabled"}}

	set, err := rec.Dump(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	set = reload(t, set)
	find(t, set, "setting", "mgmt")["led_enabled"] = true

	plan, err := rec.Plan(t.Context(), set)
	if err != nil {
		t.Fatal(err)
	}

	applyErr := rec.Apply(t.Context(), plan)
	if applyErr == nil {
		t.Fatal("apply said nothing about a value the console kept")
	}

	if !errors.Is(applyErr, reconcile.ErrKept) ||
		!strings.Contains(applyErr.Error(), "led_enabled: sent true, kept false") {
		t.Fatalf("apply = %v, want the kept value named", applyErr)
	}
}

func TestApplyWritesWholeDocumentsWithTheFilesKeysOver(t *testing.T) {
	t.Parallel()

	srv, rec := house(t)

	set, err := rec.Dump(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	set = reload(t, set)
	find(t, set, "wlanconf", "backyard")["group_rekey"] = int64(7200)

	// Port 12 renamed; port 1 shut the way the controller takes it: no
	// network, a cleared reference that must reach it as the empty string.
	sw := find(t, set, "device", "02:00:5e:00:53:91")
	sw["port_overrides"] = []any{
		map[string]any{"port_idx": int64(1), "forward": "disabled", "native_networkconf_id": ""},
		map[string]any{"port_idx": int64(12), "name": "Sizzling (Sonos)"},
	}
	find(t, set, "setting", "mdns")["enabled_for_network_ids"] = []any{"Things"}

	// The LED colour of "no network" changed: the list goes back whole,
	// Services as its id and none as the word it is.
	lighting := asList(t, find(t, set, "setting", "ether_lighting")["network_defaults"])
	asMap(t, lighting[1])["raw_color_hex"] = "000000"

	// A setting the console has never stored is created by setting it.
	set.Kinds["setting"] = append(set.Kinds["setting"], console.Document{"key": "super_sdn", "enabled": true})

	set.Kinds["networkconf"] = append(
		set.Kinds["networkconf"],
		console.Document{
			"name":             "Guests",
			"purpose":          "corporate",
			"vlan_enabled":     true,
			"vlan":             int64(40),
			"firewall_zone_id": "Internal",
		},
	)
	set.Kinds["wlanconf"] = append(
		set.Kinds["wlanconf"],
		console.Document{
			"name":           "les_amis",
			"networkconf_id": "Guests",
			"usergroup_id":   "Default",
			"ap_group_ids":   []any{"All APs"},
		},
	)

	plan, err := rec.Plan(t.Context(), set)
	if err != nil {
		t.Fatal(err)
	}

	if applyErr := rec.Apply(t.Context(), plan); applyErr != nil {
		t.Fatalf("Apply: %v", applyErr)
	}

	want := []string{
		"POST api/s/default/rest/networkconf",
		"POST api/s/default/rest/wlanconf",
		"PUT api/s/default/rest/wlanconf/" + consoletest.WLANID,
		"PUT api/s/default/set/setting/super_sdn",
		"PUT api/s/default/set/setting/ether_lighting",
		"PUT api/s/default/set/setting/mdns",
		"PUT api/s/default/rest/device/" + consoletest.SwitchID,
	}
	if strings.Join(srv.Requests, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(srv.Requests, "\n"), strings.Join(want, "\n"))
	}

	guests := srv.Bodies[0]
	if guests["firewall_zone_id"] != consoletest.ZoneID {
		t.Fatalf("create sent the zone by name: %v", guests)
	}

	networks := srv.Documents("networkconf")
	if len(networks) != 3 {
		t.Fatalf("networks = %v, want the created one as well", networks)
	}

	lesAmis := srv.Bodies[1]
	if lesAmis["networkconf_id"] != networks[2]["_id"] {
		t.Fatalf("the new WLAN does not point at the new network's id: %v", lesAmis)
	}

	if groups := asList(t, lesAmis["ap_group_ids"]); len(groups) != 1 || groups[0] != consoletest.GroupID {
		t.Fatalf("ap_group_ids not resolved: %v", lesAmis["ap_group_ids"])
	}

	backyard := srv.Bodies[2]
	if backyard["x_passphrase"] != "not-the-real-one" || backyard["_id"] != consoletest.WLANID {
		t.Fatal("the update did not carry the controller's own keys back")
	}

	if fmt.Sprint(backyard["group_rekey"]) != "7200" {
		t.Fatalf("group_rekey = %v", backyard["group_rekey"])
	}

	mdns := srv.Bodies[5]
	if networks := asList(
		t,
		mdns["enabled_for_network_ids"],
	); len(networks) != 1 ||
		networks[0] != consoletest.ThingsID {
		t.Fatalf("enabled_for_network_ids not resolved: %v", mdns["enabled_for_network_ids"])
	}

	if sdn := srv.Setting("super_sdn"); sdn == nil || sdn["enabled"] != true {
		t.Fatalf("super_sdn = %v, want it stored", sdn)
	}

	sent := asList(t, srv.Bodies[4]["network_defaults"])
	if len(sent) != 2 || asMap(t, sent[0])["key"] != consoletest.ServicesID || asMap(t, sent[1])["key"] != "none" ||
		asMap(t, sent[1])["raw_color_hex"] != "000000" {
		t.Fatalf("network_defaults sent as %v", sent)
	}

	device := srv.Bodies[6]
	if _, runtime := device["uptime"]; runtime {
		t.Fatal("runtime state sent back to the device")
	}

	ports := asList(t, device["port_overrides"])
	if len(ports) != 2 {
		t.Fatalf("the unstated port override was dropped: %v", device["port_overrides"])
	}

	port1 := asMap(t, ports[0])
	if port1["forward"] != "disabled" || port1["native_networkconf_id"] != "" || port1["poe_mode"] != "auto" {
		t.Fatalf("port 1 = %v, want it shut, its network cleared, its PoE as it was", port1)
	}

	port12 := asMap(t, ports[1])
	if port12["name"] != "Sizzling (Sonos)" || port12["native_networkconf_id"] != consoletest.ThingsID ||
		port12["forward"] != "native" {
		t.Fatalf("port 12 = %v, want the file's name over the controller's keys", port12)
	}

	plan, err = rec.Plan(t.Context(), set)
	if err != nil {
		t.Fatal(err)
	}

	if !plan.Empty() {
		var out bytes.Buffer

		plan.Write(&out)
		t.Fatalf("plan after apply:\n%s", out.String())
	}
}

// TestApplyRanksAPolicyBeforeTheOthers: a policy created at 10000 with the
// existing one moved to 10001. The controller would number the create
// 10001 and not move the other on update, so the create goes out without
// its index, the other is not updated at all, and one batch ranks the
// pair; the console's numbers are read back and the next plan is quiet.
func TestApplyRanksAPolicyBeforeTheOthers(t *testing.T) {
	t.Parallel()

	srv, rec := house(t)

	set, err := rec.Dump(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	set = reload(t, set)
	find(t, set, "firewallpolicy", "Block Things to Services (Internal to Internal)")["index"] = int64(10001)
	set.Kinds["firewallpolicy"] = append(set.Kinds["firewallpolicy"], mediaPolicy(10000))

	plan, err := rec.Plan(t.Context(), set)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer

	plan.Write(&out)

	moved := "firewallpolicy \"Block Things to Services (Internal to Internal)\"\n  ~ index: 10000 -> 10001\n"
	if !strings.Contains(out.String(), moved) || !strings.Contains(out.String(), "2 changes.") {
		t.Fatalf("plan:\n%s", out.String())
	}

	if applyErr := rec.Apply(t.Context(), plan); applyErr != nil {
		t.Fatalf("Apply: %v", applyErr)
	}

	want := []string{
		"POST v2/api/site/default/firewall-policies",
		"PUT v2/api/site/default/firewall-policies/batch-reorder",
	}
	if strings.Join(srv.Requests, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(srv.Requests, "\n"), strings.Join(want, "\n"))
	}

	newID, ok := srv.Bodies[0]["_id"].(string)
	if !ok || fmt.Sprint(asList(t, srv.Bodies[1]["before_predefined_ids"])) != "["+newID+" "+consoletest.PolicyID+"]" {
		t.Fatalf("the batch does not put the new policy first: %v", srv.Bodies[1])
	}

	for _, doc := range srv.Documents("firewall-policies") {
		switch doc["_id"] {
		case newID:
			if fmt.Sprint(doc["index"]) != "10000" {
				t.Fatalf("the new policy is not first: %v", doc)
			}
		case consoletest.PolicyID:
			if fmt.Sprint(doc["index"]) != "10001" {
				t.Fatalf("the old policy did not move: %v", doc)
			}
		}
	}

	plan, err = rec.Plan(t.Context(), set)
	if err != nil {
		t.Fatal(err)
	}

	if !plan.Empty() {
		out.Reset()
		plan.Write(&out)
		t.Fatalf("plan after apply:\n%s", out.String())
	}
}

// TestApplyRefusesARankTheConsoleDidNotGive: the controller numbers a pair
// from 10000 with no gaps; a file that asks for 10002 behind one policy
// gets 10001, and apply says so rather than report the file applied.
func TestApplyRefusesARankTheConsoleDidNotGive(t *testing.T) {
	t.Parallel()

	_, rec := house(t)

	set, err := rec.Dump(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	set = reload(t, set)
	set.Kinds["firewallpolicy"] = append(set.Kinds["firewallpolicy"], mediaPolicy(10002))

	plan, err := rec.Plan(t.Context(), set)
	if err != nil {
		t.Fatal(err)
	}

	applyErr := rec.Apply(t.Context(), plan)
	if applyErr == nil {
		t.Fatal("apply said nothing about a rank the console did not give")
	}

	numbered := "\"Allow Guests Media (Internal to Internal)\": index 10002, numbered 10001"
	if !errors.Is(applyErr, reconcile.ErrKept) || !strings.Contains(applyErr.Error(), numbered) {
		t.Fatalf("apply = %v, want the rank the console gave named", applyErr)
	}
}

// mediaPolicy is a user policy in the Internal pair at the given rank.
func mediaPolicy(rank int64) console.Document {
	return console.Document{
		"name":    "Allow Guests Media",
		"action":  "ALLOW",
		"enabled": true,
		"index":   rank,
		"source": map[string]any{
			"zone_id":         "Internal",
			"matching_target": "NETWORK",
			"network_ids":     []any{"Things"},
		},
		"destination": map[string]any{
			"zone_id":         "Internal",
			"matching_target": "NETWORK",
			"network_ids":     []any{"Services"},
		},
	}
}

// TestApplyRefusesWhatTheConsoleKeptOnCreate: the controller's answer to a
// create is read like an update's; a key it dropped is named, not passed
// over to the next plan.
func TestApplyRefusesWhatTheConsoleKeptOnCreate(t *testing.T) {
	t.Parallel()

	srv, rec := house(t)
	srv.Stubborn = map[string][]string{"firewall-policies": {"logging"}}

	set, err := rec.Dump(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	set = reload(t, set)

	media := mediaPolicy(10001)
	media["logging"] = true
	set.Kinds["firewallpolicy"] = append(set.Kinds["firewallpolicy"], media)

	plan, err := rec.Plan(t.Context(), set)
	if err != nil {
		t.Fatal(err)
	}

	applyErr := rec.Apply(t.Context(), plan)
	if applyErr == nil {
		t.Fatal("apply said nothing about a key the console dropped on create")
	}

	if !errors.Is(applyErr, reconcile.ErrKept) || !strings.Contains(applyErr.Error(), "logging: sent true, kept null") {
		t.Fatalf("apply = %v, want the dropped key named", applyErr)
	}
}

// TestApplyRanksAPolicyAfterTheBuiltIn: a file ranked behind the pair's
// built-in policies goes in the batch's after list, and the number read
// back is the one behind them. The fake numbers the after list from the
// last built-in on; no console observed had a policy there, so the
// controller's own numbering of that list is unobserved.
func TestApplyRanksAPolicyAfterTheBuiltIn(t *testing.T) {
	t.Parallel()

	srv, rec := house(t)

	set, err := rec.Dump(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	set = reload(t, set)
	set.Kinds["firewallpolicy"] = append(set.Kinds["firewallpolicy"], mediaPolicy(30001))

	plan, err := rec.Plan(t.Context(), set)
	if err != nil {
		t.Fatal(err)
	}

	if applyErr := rec.Apply(t.Context(), plan); applyErr != nil {
		t.Fatalf("Apply: %v", applyErr)
	}

	newID, ok := srv.Bodies[0]["_id"].(string)
	if !ok || fmt.Sprint(asList(t, srv.Bodies[1]["before_predefined_ids"])) != "["+consoletest.PolicyID+"]" ||
		fmt.Sprint(asList(t, srv.Bodies[1]["after_predefined_ids"])) != "["+newID+"]" {
		t.Fatalf("the batch does not put the new policy after the built-in: %v", srv.Bodies[1])
	}

	plan, err = rec.Plan(t.Context(), set)
	if err != nil {
		t.Fatal(err)
	}

	if !plan.Empty() {
		var out bytes.Buffer

		plan.Write(&out)
		t.Fatalf("plan after apply:\n%s", out.String())
	}
}

// TestApplyDoesNotRankWhatDidNotMove: an update that changes no rank is
// one PUT and no batch; the pair's order is sent only when a file moves a
// policy in it.
func TestApplyDoesNotRankWhatDidNotMove(t *testing.T) {
	t.Parallel()

	srv, rec := house(t)

	set, err := rec.Dump(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	set = reload(t, set)
	find(t, set, "firewallpolicy", "Block Things to Services (Internal to Internal)")["logging"] = false

	plan, err := rec.Plan(t.Context(), set)
	if err != nil {
		t.Fatal(err)
	}

	if applyErr := rec.Apply(t.Context(), plan); applyErr != nil {
		t.Fatalf("Apply: %v", applyErr)
	}

	want := "PUT v2/api/site/default/firewall-policies/" + consoletest.PolicyID
	if strings.Join(srv.Requests, "\n") != want {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(srv.Requests, "\n"), want)
	}
}
