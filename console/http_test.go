package console_test

import (
	"errors"
	"testing"
	"time"

	"github.com/mycophonic/primordium/fault"

	"github.com/duncanite/go-unifi/console"
)

func TestHTTPClientRejectsAFingerprintThatIsNotASHA256(t *testing.T) {
	t.Parallel()

	for _, fingerprint := range []string{"zz", "abcd", "AB:CD"} {
		_, err := console.HTTPClient(fingerprint, false, time.Second)
		if !errors.Is(err, fault.ErrInvalidArgument) {
			t.Fatalf("HTTPClient(%q) err = %v, want ErrInvalidArgument", fingerprint, err)
		}
	}
}

func TestHTTPClientTakesAColonSeparatedFingerprint(t *testing.T) {
	t.Parallel()

	const sum = "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99"

	client, err := console.HTTPClient(sum, false, time.Second)
	if err != nil {
		t.Fatalf("HTTPClient: %v", err)
	}

	if client.Timeout != time.Second {
		t.Fatalf("Timeout = %v, want 1s", client.Timeout)
	}
}

func TestHTTPClientWithNeitherUsesTheSystemRoots(t *testing.T) {
	t.Parallel()

	client, err := console.HTTPClient("", false, time.Second)
	if err != nil {
		t.Fatalf("HTTPClient: %v", err)
	}

	if client.Transport == nil {
		t.Fatal("Transport is nil, want a clone of the default")
	}
}
