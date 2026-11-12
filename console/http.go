package console

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mycophonic/primordium/fault"
)

// HTTPClient returns the client New takes, with the console's TLS in place
// of the process default and timeout bounding each call. The console's
// certificate is self-signed, so the system roots alone refuse it:
// fingerprint, the certificate's SHA-256 in hex, pins it instead, and
// insecure accepts any certificate, for the run that reads the fingerprint
// off. With neither, the system roots apply. The transport is a clone of
// http.DefaultTransport, so a wrapper around it (primordium's own round
// tripper, under app.Main) lends its pool settings and nothing else.
func HTTPClient(fingerprint string, insecure bool, timeout time.Duration) (*http.Client, error) {
	tlsConfig, err := tlsConfigFor(fingerprint, insecure)
	if err != nil {
		return nil, err
	}

	cloner, ok := http.DefaultTransport.(interface{ Clone() *http.Transport })
	if !ok {
		return nil, fmt.Errorf("%w: the default transport cannot be cloned", fault.ErrSystemFailure)
	}

	transport := cloner.Clone()
	transport.TLSClientConfig = tlsConfig

	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

// tlsConfigFor verifies the console's certificate by its fingerprint when
// one is given, by the system roots otherwise; insecure verifies nothing.
func tlsConfigFor(fingerprint string, insecure bool) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12}

	if insecure {
		config.InsecureSkipVerify = true // #nosec G402 -- the caller asked for it, for the run that reads the fingerprint

		return config, nil
	}

	if fingerprint == "" {
		return config, nil
	}

	pin, err := hex.DecodeString(strings.ToLower(strings.NewReplacer(":", "", " ", "").Replace(fingerprint)))
	if err != nil || len(pin) != sha256.Size {
		return nil, fmt.Errorf("%w: fingerprint %q is not a hex SHA-256", fault.ErrInvalidArgument, fingerprint)
	}

	config.InsecureSkipVerify = true // #nosec G402 -- VerifyConnection below pins the certificate instead
	config.VerifyConnection = func(state tls.ConnectionState) error {
		for _, cert := range state.PeerCertificates {
			sum := sha256.Sum256(cert.Raw)
			if hex.EncodeToString(sum[:]) == hex.EncodeToString(pin) {
				return nil
			}
		}

		return fmt.Errorf(
			"%w: the console's certificate does not match the pinned fingerprint",
			fault.ErrAuthenticationFailure,
		)
	}

	return config, nil
}
