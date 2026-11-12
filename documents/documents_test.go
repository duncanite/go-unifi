package documents_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forkcloser/xos"
	"github.com/mycophonic/primordium/filesystem"

	"github.com/duncanite/go-unifi/console"
	"github.com/duncanite/go-unifi/documents"
)

func kinds() []console.Kind {
	return console.Kinds()
}

func kind(t *testing.T, name string) console.Kind {
	t.Helper()

	for _, k := range console.Kinds() {
		if k.Name == name {
			return k
		}
	}

	t.Fatalf("no kind %q", name)

	return console.Kind{}
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

func write(t *testing.T, dir, kind, name, body string) {
	t.Helper()

	target := filepath.Join(dir, kind)
	if err := os.MkdirAll(target, filesystem.DirPermissionsDefault); err != nil {
		t.Fatal(err)
	}

	if err := xos.WriteFile(
		filepath.Join(target, name+documents.Extension),
		[]byte(body),
		filesystem.FilePermissionsDefault,
	); err != nil {
		t.Fatal(err)
	}
}

func TestSlug(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"Services":          "services",
		"Pantry Switch":     "pantry-switch",
		"02:00:5e:00:53:91": "02-00-5e-00-53-91",
		"Main (WAN1)":       "main-wan1",
		"":                  "unnamed",
		"--x--":             "x",
	}

	for in, want := range cases {
		if got := documents.Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadRefusesWhatIsNotAFilesToState(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		kind, body, want string
	}{
		"secret":       {"wlanconf", "name: a\nx_passphrase: hunter2\n", "secret"},
		"owned":        {"networkconf", "name: a\n_id: abc\n", "controller's own"},
		"no identity":  {"networkconf", "purpose: corporate\n", `named by "name"`},
		"runtime":      {"device", "mac: aa\nuptime: 3\n", "not a key the controller takes"},
		"not a map":    {"networkconf", "- a\n", "parsing"},
		"setting name": {"setting", "name: mgmt\n", `named by "key"`},
		"unqualified":  {"firewallpolicy", "name: Allow All Traffic\naction: ALLOW\n", "source.zone_id"},
		"derived": {
			"firewallpolicy",
			"name: Allow Port Forward ssh\norigin_type: port_forward\nsource: {zone_id: External}\ndestination: {zone_id: Internal}\n",
			"derived from another document",
		},
		"console's own": {"setting", "key: peer_to_peer\nssid: x\n", "console's own"},
		"clear secret":  {"wlanconf", "name: a\nsae_psk: []\n", "secret"},
		"reported":      {"setting", "key: ips\nlast_alert_id: \"\"\n", "controller's own"},
		"in an entry":   {"device", "mac: aa\nradio_table:\n  - name: ra0\n    radio_caps: 1\n", "controller's own"},
		"keyless entry": {"device", "mac: aa\nport_overrides:\n  - name: x\n    port_index: 1\n", `without "port_idx"`},
		"mirrored":      {"firewallzone", "name: Lab\nnetwork_ids: []\n", "controller's view"},
		"nested secret": {
			"firewallpolicy",
			"name: a\nsource: {zone_id: A, x_token: t}\ndestination: {zone_id: B}\n",
			"secret",
		},
	}

	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			write(t, dir, tc.kind, "one", tc.body)

			_, err := documents.Load(dir, kinds())
			if !errors.Is(err, documents.ErrInvalidDocument) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want ErrInvalidDocument mentioning %q", err, tc.want)
			}
		})
	}
}

// TestLoadRefusesAFileThatIsNotADocument: a .yml is not silently nothing.
func TestLoadRefusesAFileThatIsNotADocument(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, dir, "networkconf", "a", "name: Services\n")

	if err := xos.WriteFile(
		filepath.Join(dir, "networkconf", "b.yml"),
		[]byte("name: Things\n"),
		filesystem.FilePermissionsDefault,
	); err != nil {
		t.Fatal(err)
	}

	_, err := documents.Load(dir, kinds())
	if !errors.Is(err, documents.ErrInvalidDocument) || !strings.Contains(err.Error(), "b.yml is not a document") {
		t.Fatalf("err = %v, want the .yml refused", err)
	}
}

// TestWriteReplacesTheDirectory: a document gone from the set is a file
// gone from the directory, so it cannot plan as a create later.
func TestWriteReplacesTheDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	kind := kinds()[0]

	both := []console.Document{{"name": "Services"}, {"name": "Guests"}}
	if _, err := documents.Write(dir, kind, both); err != nil {
		t.Fatal(err)
	}

	if _, err := documents.Write(dir, kind, both[:1]); err != nil {
		t.Fatal(err)
	}

	set, err := documents.Load(dir, kinds())
	if err != nil {
		t.Fatal(err)
	}

	if docs := set.Kinds[kind.Name]; len(docs) != 1 || docs[0]["name"] != "Services" {
		t.Fatalf("after the second write the directory holds %v, want Services alone", docs)
	}
}

// TestWriteRefusesOneFileForTwoIdentities: Guests and guests would be one
// file; neither is written over the other without a word.
func TestWriteRefusesOneFileForTwoIdentities(t *testing.T) {
	t.Parallel()

	_, err := documents.Write(t.TempDir(), kinds()[0], []console.Document{{"name": "Guests"}, {"name": "guests"}})
	if err == nil {
		t.Fatal("two identities wrote one file without a word")
	}

	if !errors.Is(err, documents.ErrInvalidDocument) || !strings.Contains(err.Error(), "both write to") {
		t.Fatalf("err = %v, want the collision refused", err)
	}
}

func TestLoadRefusesTwoFilesWithOneIdentity(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, dir, "networkconf", "a", "name: Services\n")
	write(t, dir, "networkconf", "b", "name: Services\n")

	_, err := documents.Load(dir, kinds())
	if !errors.Is(err, documents.ErrInvalidDocument) || !strings.Contains(err.Error(), "both declare") {
		t.Fatalf("err = %v, want the duplicate refused", err)
	}
}

func TestLoadIsCanonicalAndEmptyIsFine(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(
		t,
		dir,
		"networkconf",
		"services",
		"name: Services\ndhcpd_leasetime: 3600\nratio: 1.5\nflags: [a, 2]\nnested: {k: v}\n",
	)

	set, err := documents.Load(dir, kinds())
	if err != nil {
		t.Fatal(err)
	}

	docs := set.Kinds["networkconf"]
	if len(docs) != 1 || len(set.Kinds) != 1 {
		t.Fatalf("set = %v", set.Kinds)
	}

	doc := docs[0]
	if doc["dhcpd_leasetime"] != int64(3600) || doc["ratio"] != 1.5 {
		t.Fatalf(
			"numbers not canonical: %T %v, %T %v",
			doc["dhcpd_leasetime"],
			doc["dhcpd_leasetime"],
			doc["ratio"],
			doc["ratio"],
		)
	}

	if flags := asList(t, doc["flags"]); len(flags) != 2 || flags[1] != int64(2) {
		t.Fatalf("list not canonical: %#v", doc["flags"])
	}

	if nested := asMap(t, doc["nested"]); nested["k"] != "v" {
		t.Fatalf("map not canonical: %#v", doc["nested"])
	}

	if !set.Declares("networkconf", "Services") || set.Declares("networkconf", "Things") ||
		set.Declares("wlanconf", "Services") {
		t.Fatal("Declares answers wrong")
	}

	empty, err := documents.Load(t.TempDir(), kinds())
	if err != nil || len(empty.Kinds) != 0 {
		t.Fatalf("empty dir: set = %v, err = %v", empty, err)
	}
}

func TestWriteThenLoadRoundTrips(t *testing.T) {
	t.Parallel()

	var device console.Kind

	for _, k := range kinds() {
		if k.Name == "device" {
			device = k
		}
	}

	doc := console.Document{
		"name": "Pantry Switch", "mac": "02:00:5e:00:53:91", "led_override": "off",
		"port_overrides": []any{map[string]any{"port_idx": int64(1), "name": "AP", "poe_mode": "auto"}},
	}

	dir := t.TempDir()
	if _, err := documents.Write(dir, device, []console.Document{doc}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "device", "02-00-5e-00-53-91.yaml")

	data, err := xos.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(
		string(data),
		"mac: 02:00:5e:00:53:91\nled_override: \"off\"\nname: Pantry Switch\nport_overrides:\n",
	) {
		t.Fatalf("identity first, then sorted; got:\n%s", data)
	}

	set, err := documents.Load(dir, kinds())
	if err != nil {
		t.Fatal(err)
	}

	devices := set.Kinds["device"]
	if len(devices) != 1 {
		t.Fatalf("devices = %v", devices)
	}

	got := devices[0]
	if got["name"] != "Pantry Switch" || got["led_override"] != "off" {
		t.Fatalf("round trip lost keys: %v", got)
	}

	ports := asList(t, got["port_overrides"])
	if len(ports) != 1 {
		t.Fatalf("round trip lost the list: %v", got["port_overrides"])
	}

	if entry := asMap(t, ports[0]); entry["port_idx"] != int64(1) || entry["poe_mode"] != "auto" {
		t.Fatalf("round trip changed the entry: %#v", ports[0])
	}
}

// TestWriteKeepsTheComments: a reader's notes in a file survive the dump
// that rewrites it, each where it was; the one on a key that is gone is
// named.
func TestWriteKeepsTheComments(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	device := kind(t, "device")
	path := filepath.Join(dir, "device", "02-00-5e-00-53-91.yaml")

	annotated := `# The switch in the garage.
mac: 02:00:5e:00:53:91
led_override: "off" # dark, like the APs
name: Pantry Switch
port_overrides:
  - name: AP
    port_idx: 1
  # Sonos, on Things.
  - name: Sizzling # the kitchen one
    port_idx: 12
snmp_location: garage # soon gone
# End.
`

	if err := os.MkdirAll(filepath.Dir(path), filesystem.DirPermissionsDefault); err != nil {
		t.Fatal(err)
	}

	if err := xos.WriteFile(path, []byte(annotated), filesystem.FilePermissionsDefault); err != nil {
		t.Fatal(err)
	}

	// A new port ahead of 12, a change on 12, the LED back on, the SNMP
	// location dropped: the comments follow their keys, not their lines.
	orphans, err := documents.Write(dir, device, []console.Document{{
		"mac":          "02:00:5e:00:53:91",
		"name":         "Pantry Switch",
		"led_override": "on",
		"port_overrides": []any{
			map[string]any{"port_idx": int64(1), "name": "AP"},
			map[string]any{"port_idx": int64(5), "name": "Printer"},
			map[string]any{"port_idx": int64(12), "name": "Sizzling (Sonos)"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}

	if len(orphans) != 1 || orphans[0] != path+": snmp_location" {
		t.Fatalf("orphans = %q, want the one on snmp_location", orphans)
	}

	data, err := xos.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	want := `# The switch in the garage.
mac: 02:00:5e:00:53:91
led_override: "on" # dark, like the APs
name: Pantry Switch
port_overrides:
  - name: AP
    port_idx: 1
  - name: Printer
    port_idx: 5
  # Sonos, on Things.
  - name: Sizzling (Sonos) # the kitchen one
    port_idx: 12
# End.
`
	if string(data) != want {
		t.Fatalf("file:\n%s\nwant:\n%s", data, want)
	}
}
