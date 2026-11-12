// Package console is the slice of the controller's private Network API the
// tool uses: collections of documents under rest/, the device list under
// stat/, settings written through set/, and the v2 lists references point
// at. A document is the controller's JSON object as it is, nothing renamed.
package console

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/mycophonic/primordium/fault"
)

// DefaultSite is the one site a console has unless someone made more.
const DefaultSite = "default"

// maxResponseBytes caps a response body; stat/device for a house is a few
// hundred kilobytes.
const maxResponseBytes = 32 << 20

// Document is one controller object, keys as the controller spells them.
type Document map[string]any

// Client talks to one console's Network application with an API key.
type Client struct {
	http   *http.Client
	base   *url.URL
	apiKey string
	site   string
}

// New returns a client for the console at origin (scheme, host and port, as in
// https://192.168.1.1), the Network application behind UniFi OS's proxy.
func New(httpClient *http.Client, origin, apiKey, site string) (*Client, error) {
	base, err := url.Parse(strings.TrimSuffix(origin, "/") + "/proxy/network/")
	if err != nil {
		return nil, fmt.Errorf("%w: console URL %q: %w", fault.ErrInvalidArgument, origin, err)
	}

	if apiKey == "" {
		return nil, fmt.Errorf("%w: empty API key", fault.ErrInvalidArgument)
	}

	if site == "" {
		site = DefaultSite
	}

	return &Client{http: httpClient, base: base, apiKey: apiKey, site: site}, nil
}

// List returns every document of a kind.
func (c *Client) List(ctx context.Context, kind Kind) ([]Document, error) {
	if kind.v2 {
		var docs []Document
		if err := c.do(ctx, http.MethodGet, c.v2Path(kind.list), nil, &docs); err != nil {
			return nil, err
		}

		return docs, nil
	}

	return c.v1(ctx, http.MethodGet, c.v1Path(kind.list), nil)
}

// Create adds a document to a kind and returns it as the controller stored
// it, id included. A kind without a create endpoint refuses.
func (c *Client) Create(ctx context.Context, kind Kind, doc Document) (Document, error) {
	if kind.create == "" {
		return nil, fmt.Errorf("%w: %s documents are not created through the API", fault.ErrInvalidArgument, kind.Name)
	}

	if kind.v2 {
		var created Document
		if err := c.do(ctx, http.MethodPost, c.v2Path(kind.create), doc, &created); err != nil {
			return nil, err
		}

		return created, nil
	}

	docs, err := c.v1(ctx, http.MethodPost, c.v1Path(kind.create), doc)
	if err != nil {
		return nil, err
	}

	if len(docs) != 1 {
		return nil, fmt.Errorf(
			"%w: creating %s returned %d documents",
			fault.ErrUnacceptableResponse,
			kind.Name,
			len(docs),
		)
	}

	return docs[0], nil
}

// Update replaces the document id of a kind with doc and returns the
// document as the controller stored it, which is not always what was sent:
// the controller answers ok and keeps its own values when it will not take
// a change, and the answer is the only place that shows. The controller
// takes the whole document: send it what it holds with the changes laid
// over. A controller that answers nothing returns nil.
func (c *Client) Update(ctx context.Context, kind Kind, identifier string, doc Document) (Document, error) {
	if kind.v2 {
		var stored Document
		if err := c.do(
			ctx,
			http.MethodPut,
			c.v2Path(kind.update+"/"+url.PathEscape(identifier)),
			doc,
			&stored,
		); err != nil {
			return nil, err
		}

		return stored, nil
	}

	docs, err := c.v1(ctx, http.MethodPut, c.v1Path(kind.update+"/"+url.PathEscape(identifier)), doc)
	if err != nil {
		return nil, err
	}

	if len(docs) == 0 {
		return nil, nil //nolint:nilnil // an empty answer is an answer: nothing to compare
	}

	return docs[0], nil
}

// Order is one group's documents of a ranked kind in the order they are
// to hold: the ids ranked before the controller's own, and those after.
// Group names the group as the batch wants it, a policy's two zone ids.
type Order struct {
	Group  map[string]string
	Before []string
	After  []string
}

// Rank sends the controller the order of one group of a ranked kind and
// returns the group's documents as it numbered them. The controller
// refuses a batch that leaves out one of the group's documents.
func (c *Client) Rank(ctx context.Context, kind Kind, order Order) ([]Document, error) {
	if kind.Rank == nil {
		return nil, fmt.Errorf("%w: %s documents are not ranked through the API", fault.ErrInvalidArgument, kind.Name)
	}

	body := make(map[string]any, len(order.Group)+2)
	for key, id := range order.Group {
		body[key] = id
	}

	// Empty lists, never null: the controller reads a null as a batch
	// that leaves the documents out.
	body[kind.Rank.before] = append([]string{}, order.Before...)
	body[kind.Rank.after] = append([]string{}, order.After...)

	var docs []Document
	if err := c.do(ctx, http.MethodPut, c.v2Path(kind.Rank.path), body, &docs); err != nil {
		return nil, err
	}

	return docs, nil
}

func (c *Client) v1Path(rel string) string {
	return "api/s/" + url.PathEscape(c.site) + "/" + rel
}

func (c *Client) v2Path(rel string) string {
	return "v2/api/site/" + url.PathEscape(c.site) + "/" + rel
}

// envelope is the v1 answer: the documents, and a verdict in meta.
type envelope struct {
	Meta meta       `json:"meta"`
	Data []Document `json:"data"`
}

type meta struct {
	RC      string `json:"rc"`
	Message string `json:"msg"`
}

func (c *Client) v1(ctx context.Context, method, path string, body any) ([]Document, error) {
	var env envelope
	if err := c.do(ctx, method, path, body, &env); err != nil {
		return nil, err
	}

	if env.Meta.RC != "ok" {
		return nil, fmt.Errorf("%w: %s %s: %s", fault.ErrUnacceptableResponse, method, path, env.Meta.Message)
	}

	return env.Data, nil
}

// v2Error is how the v2 endpoints say no; a 4xx v1 answer carries its
// message in the envelope instead.
type v2Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	ref, err := url.Parse(path)
	if err != nil {
		return fmt.Errorf("%w: path %q: %w", fault.ErrInvalidArgument, path, err)
	}

	var payload io.Reader

	if body != nil {
		encoded, encodeErr := json.Marshal(body)
		if encodeErr != nil {
			return fmt.Errorf("%w: encoding request: %w", fault.ErrInvalidJSON, encodeErr)
		}

		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base.ResolveReference(ref).String(), payload)
	if err != nil {
		return fmt.Errorf("%w: %w", fault.ErrNetworkCommunication, err)
	}

	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s %s: %w", fault.ErrNetworkCommunication, method, path, err)
	}

	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%w: reading response: %w", fault.ErrNetworkCommunication, err)
	}

	if err := statusError(resp.StatusCode, resp.Status, method, path, data); err != nil {
		return err
	}

	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%w: decoding %s %s: %w", fault.ErrInvalidJSON, method, path, err)
	}

	return nil
}

// statusError turns a failing status into the fault it is, with the
// controller's own message: api.err.InvalidPayload says more than 400.
func statusError(code int, status, method, path string, data []byte) error {
	switch {
	case code == http.StatusNotFound:
		return fmt.Errorf("%w: %s %s: %s", fault.ErrNotFound, method, path, message(data))
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return fmt.Errorf("%w: %s %s: %s", fault.ErrAuthenticationFailure, method, path, status)
	case code < http.StatusOK || code >= http.StatusMultipleChoices:
		return fmt.Errorf("%w: %s %s: %s: %s", fault.ErrUnacceptableResponse, method, path, status, message(data))
	}

	return nil
}

// message digs the controller's reason out of an error body, whichever of
// its two shapes it has, and falls back to the body itself.
func message(data []byte) string {
	var env envelope
	if json.Unmarshal(data, &env) == nil && env.Meta.Message != "" {
		return env.Meta.Message
	}

	var v2 v2Error
	if json.Unmarshal(data, &v2) == nil && (v2.Code != "" || v2.Message != "") {
		return strings.TrimSpace(v2.Code + " " + v2.Message)
	}

	return string(bytes.TrimSpace(data))
}
