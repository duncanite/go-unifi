// Package consoletest is a controller for tests: an HTTP server speaking
// the slice of the Network API the tool uses, over documents held in
// memory, demanding the API key on every request.
package consoletest

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// APIKey is the one the server accepts.
const APIKey = "test-key"

const (
	prefix     = "/proxy/network/"
	v1Prefix   = "api/s/default/"
	v2Prefix   = "v2/api/site/default/"
	restPrefix = "rest"
	// firstRank is where the controller starts numbering a zone pair's
	// user policies.
	firstRank = 10000
)

// Server is the fake console.
type Server struct {
	*httptest.Server

	mu sync.Mutex
	// docs holds the v1 collections by name (networkconf, setting, device,
	// usergroup...) and the v2 ones by their path (apgroups, firewall/zone).
	docs map[string][]map[string]any
	// Stubborn names, per collection, the keys the server answers ok to
	// and keeps its own values of, as the controller does with a setting
	// it will not take.
	Stubborn map[string][]string
	// Requests is every write the server took, method and path, in order.
	Requests []string
	// Bodies is the body of every write, in the same order.
	Bodies []map[string]any
	next   int
}

// New starts a server holding docs: collection name to documents. Close it
// with the test.
func New(docs map[string][]map[string]any) *Server {
	srv := &Server{docs: make(map[string][]map[string]any, len(docs))}
	for name, list := range docs {
		srv.docs[name] = append([]map[string]any(nil), list...)
	}

	srv.Server = httptest.NewTLSServer(http.HandlerFunc(srv.handle))

	return srv
}

// Documents returns the collection as the server holds it now.
func (s *Server) Documents(collection string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]map[string]any(nil), s.docs[collection]...)
}

// Setting returns the setting of that key as the server holds it, nil when
// there is none.
func (s *Server) Setting(key string) map[string]any {
	for _, doc := range s.Documents("setting") {
		if doc["key"] == key {
			return doc
		}
	}

	return nil
}

func (s *Server) handle(writer http.ResponseWriter, req *http.Request) {
	if req.Header.Get("X-Api-Key") != APIKey {
		http.Error(writer, `{"meta":{"rc":"error","msg":"api.err.LoginRequired"}}`, http.StatusUnauthorized)

		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !strings.HasPrefix(req.URL.Path, prefix) {
		http.NotFound(writer, req)

		return
	}

	path := strings.TrimPrefix(req.URL.Path, prefix)

	switch {
	case strings.HasPrefix(path, v2Prefix):
		s.v2(writer, req, strings.TrimPrefix(path, v2Prefix))
	case strings.HasPrefix(path, v1Prefix):
		s.v1(writer, req, strings.TrimPrefix(path, v1Prefix))
	default:
		http.NotFound(writer, req)
	}
}

// v2 serves the v2 collections, the two-segment one first so that
// firewall/zone/<id> is matched to its collection: a list and a create at
// the collection, an update at the collection and id; the answers are
// plain documents.
func (s *Server) v2(writer http.ResponseWriter, req *http.Request, path string) {
	if path == "firewall-policies/batch-reorder" && req.Method == http.MethodPut {
		s.rank(writer, req)

		return
	}

	for _, collection := range []string{"firewall/zone", "firewall-policies", "apgroups"} {
		switch {
		case path == collection && req.Method == http.MethodGet:
			writeJSON(writer, http.StatusOK, s.docs[collection])

			return
		case path == collection && req.Method == http.MethodPost:
			s.createV2(writer, req, collection)

			return
		case strings.HasPrefix(path, collection+"/") && req.Method == http.MethodPut:
			s.updateV2(writer, req, collection, strings.TrimPrefix(path, collection+"/"))

			return
		}
	}

	http.Error(writer, `{"code":"NOT_FOUND","message":"no such collection"}`, http.StatusNotFound)
}

func (s *Server) v1(writer http.ResponseWriter, req *http.Request, path string) {
	parts := strings.Split(path, "/")

	switch {
	case len(parts) == 2 && parts[0] == restPrefix && req.Method == http.MethodGet:
		respond(writer, s.docs[parts[1]])
	case len(parts) == 2 && parts[0] == "stat" && parts[1] == "device" && req.Method == http.MethodGet:
		respond(writer, s.docs["device"])
	case len(parts) == 2 && parts[0] == restPrefix && req.Method == http.MethodPost:
		s.create(writer, req, parts[1])
	case len(parts) == 3 && parts[0] == restPrefix && req.Method == http.MethodPut:
		s.update(writer, req, parts[1], "_id", parts[2])
	case len(parts) == 3 && parts[0] == "set" && parts[1] == "setting" && req.Method == http.MethodPut:
		s.update(writer, req, "setting", "key", parts[2])
	default:
		fail(writer, http.StatusNotFound, "api.err.NotFound")
	}
}

func (s *Server) create(writer http.ResponseWriter, req *http.Request, collection string) {
	doc, ok := s.record(writer, req)
	if !ok {
		return
	}

	if name, isString := doc["name"].(string); !isString || name == "" {
		fail(writer, http.StatusBadRequest, "api.err.InvalidPayload")

		return
	}

	s.keep(collection, nil, doc)
	s.next++
	doc["_id"] = fmt.Sprintf("%023df", s.next)
	doc["site_id"] = "site"
	// A copy: what is stored moves on later, a rank say, and the body
	// recorded is the body as it was sent.
	s.docs[collection] = append(s.docs[collection], maps.Clone(doc))

	respond(writer, []map[string]any{doc})
}

func (s *Server) update(writer http.ResponseWriter, req *http.Request, collection, idKey, wanted string) {
	doc, ok := s.record(writer, req)
	if !ok {
		return
	}

	for i, existing := range s.docs[collection] {
		if existing[idKey] == wanted {
			s.keep(collection, existing, doc)
			maps.Copy(existing, doc)

			s.docs[collection][i] = existing
			respond(writer, []map[string]any{existing})

			return
		}
	}

	// A setting is stored the first time it is set; anything else must exist.
	if collection != "setting" {
		fail(writer, http.StatusNotFound, "api.err.NotFound")

		return
	}

	doc[idKey] = wanted
	doc["_id"] = fmt.Sprintf("%023df", len(s.docs[collection])+1)
	s.docs[collection] = append(s.docs[collection], maps.Clone(doc))
	respond(writer, []map[string]any{doc})
}

func (s *Server) createV2(writer http.ResponseWriter, req *http.Request, collection string) {
	doc, ok := s.record(writer, req)
	if !ok {
		return
	}

	s.keep(collection, nil, doc)
	doc["_id"] = fmt.Sprintf("%023df", len(s.docs[collection])+1)
	s.docs[collection] = append(s.docs[collection], maps.Clone(doc))
	writeJSON(writer, http.StatusOK, doc)
}

func (s *Server) updateV2(writer http.ResponseWriter, req *http.Request, collection, wanted string) {
	doc, ok := s.record(writer, req)
	if !ok {
		return
	}

	for i, existing := range s.docs[collection] {
		if existing["_id"] == wanted {
			maps.Copy(existing, doc)

			s.docs[collection][i] = existing
			writeJSON(writer, http.StatusOK, existing)

			return
		}
	}

	refuse(writer, http.StatusNotFound, "api.err.NotFound", "no "+collection+" "+wanted)
}

// refuse is a v2 error answer: a code and a message.
func refuse(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{"code": code, "message": message})
}

// rank is firewall-policies/batch-reorder as the controller has it: the
// zone pair's user policies, every one of them, in two lists, before and
// after the predefined; numbered from 10000 in the order given, the after
// list from the last predefined on; the answer is the pair's policies. A
// batch that leaves a user policy out, or names one of another pair, is
// refused.
func (s *Server) rank(writer http.ResponseWriter, req *http.Request) {
	batch, ok := s.record(writer, req)
	if !ok {
		return
	}

	source, destination := batch["source_zone_id"], batch["destination_zone_id"]
	listed := append(ids(batch["before_predefined_ids"]), ids(batch["after_predefined_ids"])...)

	var pair []map[string]any

	last := 0
	users := make(map[string]map[string]any)

	for _, doc := range s.docs["firewall-policies"] {
		if zone(doc, "source") != source || zone(doc, "destination") != destination {
			continue
		}

		pair = append(pair, doc)

		if doc["predefined"] == true {
			last = max(last, number(doc["index"]))
		} else {
			users[fmt.Sprint(doc["_id"])] = doc
		}
	}

	if len(listed) != len(users) {
		refuse(writer, http.StatusBadRequest, "api.err.Invalid", "every policy of the pair")

		return
	}

	before := len(ids(batch["before_predefined_ids"]))

	for i, id := range listed {
		doc, known := users[id]
		if !known {
			refuse(writer, http.StatusBadRequest, "api.err.Invalid", "not of the pair: "+id)

			return
		}

		if i < before {
			doc["index"] = firstRank + i
		} else {
			doc["index"] = last + 1 + i - before
		}
	}

	writeJSON(writer, http.StatusOK, pair)
}

func zone(doc map[string]any, side string) any {
	if half, ok := doc[side].(map[string]any); ok {
		return half["zone_id"]
	}

	return nil
}

func ids(value any) []string {
	list, ok := value.([]any)
	if !ok {
		return nil
	}

	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, fmt.Sprint(item))
	}

	return out
}

func number(value any) int {
	if n, ok := value.(json.Number); ok {
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	}

	if f, ok := value.(float64); ok {
		return int(f)
	}

	if i, ok := value.(int); ok {
		return i
	}

	return 0
}

// keep puts the existing values of the stubborn keys back into doc before
// it is stored, so the answer shows the server kept them; on a create,
// with nothing existing, a stubborn key is dropped, as the controller
// drops a key it does not take.
func (s *Server) keep(collection string, existing, doc map[string]any) {
	for _, key := range s.Stubborn[collection] {
		if value, held := existing[key]; held {
			doc[key] = value
		} else {
			delete(doc, key)
		}
	}
}

func (s *Server) record(writer http.ResponseWriter, req *http.Request) (map[string]any, bool) {
	var doc map[string]any

	dec := json.NewDecoder(req.Body)
	dec.UseNumber()

	if err := dec.Decode(&doc); err != nil || doc == nil {
		fail(writer, http.StatusBadRequest, "api.err.InvalidPayload")

		return nil, false
	}

	s.Requests = append(s.Requests, req.Method+" "+strings.TrimPrefix(req.URL.Path, prefix))
	s.Bodies = append(s.Bodies, doc)

	return doc, true
}

func respond(writer http.ResponseWriter, docs []map[string]any) {
	if docs == nil {
		docs = []map[string]any{}
	}

	writeJSON(writer, http.StatusOK, map[string]any{"meta": map[string]string{"rc": "ok"}, "data": docs})
}

func fail(writer http.ResponseWriter, status int, msg string) {
	writeJSON(writer, status, map[string]any{"meta": map[string]string{"rc": "error", "msg": msg}, "data": []any{}})
}

func writeJSON(writer http.ResponseWriter, status int, body any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(body)
}
