package console_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mycophonic/primordium/fault"

	"github.com/duncanite/go-unifi/console"
	"github.com/duncanite/go-unifi/console/consoletest"
)

func kind(t *testing.T, name string) console.Kind {
	t.Helper()

	for _, candidate := range console.Kinds() {
		if candidate.Name == name {
			return candidate
		}
	}

	t.Fatalf("no kind %q", name)

	return console.Kind{}
}

func client(t *testing.T, srv *consoletest.Server, key string) *console.Client {
	t.Helper()

	c, err := console.New(srv.Client(), srv.URL, key, console.DefaultSite)
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func TestNewRejectsEmptyKey(t *testing.T) {
	t.Parallel()

	_, err := console.New(http.DefaultClient, "https://192.168.1.1", "", "")
	if !errors.Is(err, fault.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}

func TestListV1AndV2(t *testing.T) {
	t.Parallel()

	srv := consoletest.New(consoletest.House())
	t.Cleanup(srv.Close)

	c := client(t, srv, consoletest.APIKey)

	networks, err := c.List(t.Context(), kind(t, "networkconf"))
	if err != nil {
		t.Fatalf("List networkconf: %v", err)
	}

	if len(networks) != 2 || networks[0]["name"] != "Services" {
		t.Fatalf("networks = %v", networks)
	}

	zones, err := c.List(t.Context(), kind(t, "firewallzone"))
	if err != nil {
		t.Fatalf("List firewallzone: %v", err)
	}

	if len(zones) != 3 || zones[0]["_id"] != consoletest.ZoneID {
		t.Fatalf("zones = %v", zones)
	}

	devices, err := c.List(t.Context(), kind(t, "device"))
	if err != nil {
		t.Fatalf("List device: %v", err)
	}

	if len(devices) != 2 || devices[0]["mac"] != "02:00:5e:00:53:91" {
		t.Fatalf("devices = %v", devices)
	}
}

func TestClientsAndHealthAreRead(t *testing.T) {
	t.Parallel()

	srv := consoletest.New(map[string][]map[string]any{
		"sta": {
			{"mac": "02:00:5e:00:53:10", "hostname": "laptop", "is_wired": false, "signal": -52, "rx_bytes": 1024},
			{"mac": "02:00:5e:00:53:11", "is_wired": true, "sw_port": 3},
		},
		"health": {
			{"subsystem": "wan", "status": "ok"},
			{"subsystem": "wlan", "status": "ok", "num_user": 7},
		},
	})
	t.Cleanup(srv.Close)

	c := client(t, srv, consoletest.APIKey)

	clients, err := c.Clients(t.Context())
	if err != nil {
		t.Fatalf("Clients: %v", err)
	}

	// The controller's own keys, and its numbers kept exact.
	if len(clients) != 2 || clients[0]["hostname"] != "laptop" || clients[0]["signal"] != json.Number("-52") ||
		clients[1]["is_wired"] != true {
		t.Fatalf("clients = %v", clients)
	}

	health, err := c.Health(t.Context())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}

	if len(health) != 2 || health[1]["subsystem"] != "wlan" || health[1]["num_user"] != json.Number("7") {
		t.Fatalf("health = %v", health)
	}

	// Nothing was written to the console.
	if len(srv.Requests) != 0 {
		t.Fatalf("the reads wrote: %v", srv.Requests)
	}

	// A console with neither answers with none, not with an error.
	empty := consoletest.New(nil)
	t.Cleanup(empty.Close)

	if got, err := client(t, empty, consoletest.APIKey).Clients(t.Context()); err != nil || len(got) != 0 {
		t.Fatalf("Clients of an empty console = %v, %v", got, err)
	}

	// The key is asked for these as for everything else.
	if _, err := client(t, srv, "wrong").Health(t.Context()); !errors.Is(err, fault.ErrAuthenticationFailure) {
		t.Fatalf("Health with a wrong key: %v", err)
	}
}

func TestARedirectIsNotFollowed(t *testing.T) {
	t.Parallel()

	// Where the redirect points: anything asked of it has the key.
	asked := make(chan string, 1)
	elsewhere := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		select {
		case asked <- req.Header.Get("X-Api-Key"):
		default:
		}
	}))
	t.Cleanup(elsewhere.Close)

	for _, status := range []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
			http.Redirect(writer, req, elsewhere.URL+req.URL.Path, status)
		}))
		t.Cleanup(srv.Close)

		theirs := srv.Client()

		c, err := console.New(theirs, srv.URL, consoletest.APIKey, "")
		if err != nil {
			t.Fatal(err)
		}

		if _, err = c.List(t.Context(), kind(t, "networkconf")); err == nil ||
			!strings.Contains(err.Error(), "redirect") {
			t.Fatalf("%d: List = %v, want a refusal of the redirect", status, err)
		}

		// The caller's own client is as it was.
		if theirs.CheckRedirect != nil {
			t.Fatalf("%d: the caller's client was changed", status)
		}
	}

	select {
	case key := <-asked:
		t.Fatalf("the place a redirect named was asked, with the key %q", key)
	default:
	}
}

func TestAnAnswerThatIsNoErrorOfTheControllersIsCutShort(t *testing.T) {
	t.Parallel()

	page := strings.Repeat("a proxy's page, é and all. ", 40000)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte(page))
	}))
	t.Cleanup(srv.Close)

	c, err := console.New(srv.Client(), srv.URL, consoletest.APIKey, "")
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.List(t.Context(), kind(t, "networkconf"))

	const bound = 1024

	if err == nil || len(err.Error()) > bound || !strings.Contains(err.Error(), "502") ||
		!strings.Contains(err.Error(), "a proxy's page") || !utf8.ValidString(err.Error()) {
		t.Fatalf("an error of %d bytes for a page of %d: %.200v", len(fmt.Sprint(err)), len(page), err)
	}
}

func TestWrongKeyIsAuthenticationFailure(t *testing.T) {
	t.Parallel()

	srv := consoletest.New(consoletest.House())
	t.Cleanup(srv.Close)

	_, err := client(t, srv, "wrong").List(t.Context(), kind(t, "networkconf"))
	if !errors.Is(err, fault.ErrAuthenticationFailure) {
		t.Fatalf("err = %v, want ErrAuthenticationFailure", err)
	}
}

func TestErrorsCarryTheControllersMessage(t *testing.T) {
	t.Parallel()

	srv := consoletest.New(consoletest.House())
	t.Cleanup(srv.Close)

	c := client(t, srv, consoletest.APIKey)

	_, err := c.Update(t.Context(), kind(t, "networkconf"), "000000000000000000000000", console.Document{"name": "x"})
	if err == nil {
		t.Fatal("an update of a document that is not there went through")
	}

	if !errors.Is(err, fault.ErrNotFound) || !strings.Contains(err.Error(), "api.err.NotFound") {
		t.Fatalf("err = %v, want not found with the controller's message", err)
	}

	_, err = c.Create(t.Context(), kind(t, "networkconf"), console.Document{"purpose": "corporate"})
	if !errors.Is(err, fault.ErrUnacceptableResponse) || !strings.Contains(err.Error(), "api.err.InvalidPayload") {
		t.Fatalf("err = %v, want unacceptable response with the controller's message", err)
	}

	_, err = c.Create(t.Context(), kind(t, "setting"), console.Document{"key": "mgmt"})
	if !errors.Is(err, fault.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument: settings are not created", err)
	}
}

func TestCreateAndUpdate(t *testing.T) {
	t.Parallel()

	srv := consoletest.New(consoletest.House())
	t.Cleanup(srv.Close)

	c := client(t, srv, consoletest.APIKey)

	created, err := c.Create(
		t.Context(),
		kind(t, "networkconf"),
		console.Document{"name": "Guests", "purpose": "corporate"},
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if id, ok := created["_id"].(string); !ok || len(id) != 24 {
		t.Fatalf("created = %v, want an id", created)
	}

	stored, err := c.Update(
		t.Context(),
		kind(t, "setting"),
		"mgmt",
		console.Document{"key": "mgmt", "led_enabled": true},
	)
	if err != nil {
		t.Fatalf("Update setting: %v", err)
	}

	if stored["led_enabled"] != true {
		t.Fatalf("the answer is not what was stored: %v", stored)
	}

	if got := srv.Requests; len(got) != 2 || got[1] != "PUT api/s/default/set/setting/mgmt" {
		t.Fatalf("requests = %v", got)
	}

	if mgmt := srv.Setting("mgmt"); mgmt["led_enabled"] != true {
		t.Fatalf("setting not updated: %v", mgmt)
	}
}

func TestKindsManagedFirstInApplyOrder(t *testing.T) {
	t.Parallel()

	var order []string

	for _, k := range console.Kinds() {
		if k.Managed() {
			order = append(order, k.Name)
		}
	}

	want := "firewallzone apgroup networkconf wlanconf portforward firewallpolicy setting device"
	if got := strings.Join(order, " "); got != want {
		t.Fatalf("managed order = %q, want %q", got, want)
	}

	if !console.Owned("_id") || !console.Owned("attr_hidden_id") || !console.Owned("x_passphrase") ||
		console.Owned("name") {
		t.Fatal("Owned does not sort the controller's keys from the file's")
	}
}

// TestRankIsOneBatchPerPair: the pair's user policies in the order given,
// numbered by the console from 10000; a batch that leaves one out is
// refused with the console's message, and a kind that is not ranked
// refuses before any request.
func TestRankIsOneBatchPerPair(t *testing.T) {
	t.Parallel()

	srv := consoletest.New(consoletest.House())
	t.Cleanup(srv.Close)

	c := client(t, srv, consoletest.APIKey)
	policies := kind(t, "firewallpolicy")
	pair := map[string]string{"source_zone_id": consoletest.ZoneID, "destination_zone_id": consoletest.ZoneID}

	docs, err := c.Rank(t.Context(), policies, console.Order{Group: pair, Before: []string{consoletest.PolicyID}})
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}

	if len(docs) != 2 {
		t.Fatalf("the answer is the pair's policies, predefined included: %d", len(docs))
	}

	_, err = c.Rank(t.Context(), policies, console.Order{Group: pair})
	if !errors.Is(err, fault.ErrUnacceptableResponse) || !strings.Contains(err.Error(), "every policy of the pair") {
		t.Fatalf("a batch that leaves a policy out = %v", err)
	}

	_, err = c.Rank(t.Context(), kind(t, "networkconf"), console.Order{})
	if !errors.Is(err, fault.ErrInvalidArgument) {
		t.Fatalf("ranking an unranked kind = %v", err)
	}
}
