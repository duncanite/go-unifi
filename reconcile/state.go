package reconcile

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/duncanite/go-unifi/console"
	"github.com/duncanite/go-unifi/documents"
)

// state is the console as last read: every kind's documents, and the two
// directions of the id-to-name map the references need.
type state struct {
	current map[string][]console.Document
	names   map[string]map[string]string // kind, id -> identity
	ids     map[string]map[string]string // kind, identity -> id
	// words is every value that is not an id the console holds at a
	// reference, by kind and path: its own words for no network, "" on a
	// port and none in the LED colour list. A file may use them too.
	words map[string]map[string]map[string]struct{}
	// index is each kind's documents by identity as a file spells it,
	// built on the first find and dropped when a document is added: a
	// qualified identity costs an export, and find runs once per file.
	index map[string]map[string]int
}

func (r *Reconciler) fetch(ctx context.Context) (*state, error) {
	snapshot := &state{
		current: make(map[string][]console.Document),
		names:   make(map[string]map[string]string),
		ids:     make(map[string]map[string]string),
		words:   make(map[string]map[string]map[string]struct{}),
		index:   make(map[string]map[string]int),
	}

	for _, kind := range console.Kinds() {
		docs, err := r.Console.List(ctx, kind)
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", kind.Name, err)
		}

		for _, doc := range docs {
			canonical, err := documents.Canonical(doc)
			if err != nil {
				return nil, fmt.Errorf("%s document: %w", kind.Name, err)
			}

			snapshot.add(kind, canonical)
		}

		snapshot.learn(kind)
	}

	return snapshot, nil
}

// learn records the words the console uses at the kind's references in
// place of an id, so a file may say the same.
func (st *state) learn(kind console.Kind) {
	for path := range kind.Refs {
		for _, doc := range st.current[kind.Name] {
			rewrite(maps.Clone(doc), path, func(value string) string {
				if value != "" && !objectID.MatchString(value) {
					st.word(kind.Name, path, value, true)
				}

				return value
			})
		}
	}
}

// word records a value at a kind's reference path when record is set, and
// reports whether it is known.
func (st *state) word(kind, path, value string, record bool) bool {
	if st.words[kind] == nil {
		st.words[kind] = make(map[string]map[string]struct{})
	}

	if st.words[kind][path] == nil {
		st.words[kind][path] = make(map[string]struct{})
	}

	if record {
		st.words[kind][path][value] = struct{}{}
	}

	_, known := st.words[kind][path][value]

	return known
}

// add records a document the console holds, or has just created.
func (st *state) add(kind console.Kind, doc console.Document) {
	st.current[kind.Name] = append(st.current[kind.Name], doc)
	delete(st.index, kind.Name)

	if st.names[kind.Name] == nil {
		st.names[kind.Name] = make(map[string]string)
	}

	if st.ids[kind.Name] == nil {
		st.ids[kind.Name] = make(map[string]string)
	}

	identity := documents.Identity(kind, doc)
	if id, ok := doc["_id"].(string); ok && identity != "" {
		st.names[kind.Name][id] = identity
		st.ids[kind.Name][identity] = id
	}
}

// rerank records the rank the controller gave a document of a ranked
// kind, so the snapshot holds what the console does.
func (st *state) rerank(kind console.Kind, id string, rank int64) {
	for i, doc := range st.current[kind.Name] {
		if kind.ID(doc) == id {
			next := maps.Clone(doc)
			next[kind.Rank.Key] = rank
			st.current[kind.Name][i] = next
		}
	}
}

func (st *state) find(kind console.Kind, identity string) (console.Document, bool) {
	docs := st.current[kind.Name]

	byIdentity, ok := st.index[kind.Name]
	if !ok {
		byIdentity = make(map[string]int, len(docs))

		for i, doc := range docs {
			if _, taken := byIdentity[st.identity(kind, doc)]; !taken {
				byIdentity[st.identity(kind, doc)] = i
			}
		}

		st.index[kind.Name] = byIdentity
	}

	at, found := byIdentity[identity]
	if !found || at >= len(docs) {
		return nil, false
	}

	return docs[at], true
}

// identity names a document the console holds as a file would: a
// qualifier is a reference, and a file spells it by name.
func (st *state) identity(kind console.Kind, doc console.Document) string {
	if len(kind.Qualifiers) == 0 {
		return documents.Identity(kind, doc)
	}

	return documents.Identity(kind, st.export(kind, doc))
}

// export is a document as a file states it: the controller's keys dropped
// at every depth, a device's runtime state and the keys the controller
// derives from other documents dropped, references by name.
func (st *state) export(kind console.Kind, doc console.Document) console.Document {
	out := make(console.Document, len(doc))

	for key, value := range doc {
		if console.Owned(key) || kind.Mirrored(key) || (kind.Keys != nil && !slices.Contains(kind.Keys, key)) {
			continue
		}

		out[key] = prune(value)
	}

	for path, target := range kind.Refs {
		rewrite(out, path, func(id string) string {
			if name, ok := st.names[target][id]; ok {
				return name
			}

			return id
		})
	}

	return out
}

// brackets is the entry selector in a diff's path, port_overrides[12].name,
// which strips to the reference path port_overrides.name.
var brackets = regexp.MustCompile(`\[[^\]]*\]`)

// named renders a change's diffs for a reader: every reference by the name
// of what it points at, on both sides, as the files spell it. The plan is
// computed on ids and shown in names; an id the console does not know, or
// an empty reference, is shown as it is.
func (st *state) named(kind console.Kind, diffs []Diff) []Diff {
	out := slices.Clone(diffs)

	for i := range out {
		path := brackets.ReplaceAllString(out[i].Path, "")

		for refPath, target := range kind.Refs {
			listKey, field, nested := strings.Cut(refPath, ".")

			switch {
			case path == refPath:
				out[i].Old = st.byName(target, "", out[i].Old)
				out[i].New = st.byName(target, "", out[i].New)
			case nested && path == listKey:
				out[i].Old = st.byName(target, field, out[i].Old)
				out[i].New = st.byName(target, field, out[i].New)
			}
		}
	}

	return out
}

// byName is value with every id of the target kind replaced by its name:
// a string or a list of them when field is empty, a list entry's field
// otherwise. The value is copied, never written through.
func (st *state) byName(target, field string, value any) any {
	mapping := func(id string) string {
		if name, ok := st.names[target][id]; ok {
			return name
		}

		return id
	}

	if field == "" {
		holder := map[string]any{"value": value}
		rewrite(holder, "value", mapping)

		return holder["value"]
	}

	switch typed := value.(type) {
	case map[string]any:
		next := maps.Clone(typed)
		rewrite(next, field, mapping)

		return next
	case []any:
		return rewriteEntries(typed, field, mapping)
	default:
		return value
	}
}

// file is the document as a file would state it, and whether a file would:
// not one the console owns, and, on a sparse kind, not one that is its
// identity and nothing else, whose file would enforce nothing.
func (st *state) file(kind console.Kind, doc console.Document) (console.Document, bool) {
	identity := st.identity(kind, doc)
	if identity == "" || kind.IsOwn(doc) || kind.Derived(doc) {
		return nil, false
	}

	exported := st.export(kind, doc)
	if kind.Sparse && len(exported) <= 1 {
		return nil, false
	}

	return exported, true
}

// prune is value with the controller's own keys dropped from every map in
// it, however deep: a radio's capabilities sit beside its channel in a
// list entry, and a secret may sit inside a nested map. Maps and lists are
// copied, the document read stays as it was.
func prune(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		next := make(map[string]any, len(typed))

		for key, item := range typed {
			if !console.Owned(key) {
				next[key] = prune(item)
			}
		}

		return next
	case []any:
		copied := make([]any, 0, len(typed))
		for _, item := range typed {
			copied = append(copied, prune(item))
		}

		return copied
	default:
		return value
	}
}

// resolve is a file's document as the console takes it: references by id.
// A name the console does not know but the set declares is left as the
// name, pending the create that gives it an id; with no set, it fails.
func (st *state) resolve(kind console.Kind, doc console.Document, set *documents.Set) (console.Document, error) {
	out := maps.Clone(doc)

	var failure error

	for path, target := range kind.Refs {
		rewrite(out, path, func(name string) string {
			if id, ok := st.ids[target][name]; ok {
				return id
			}

			// The empty string, an id, a word the console itself uses at
			// this reference, or a name a file declares: the same on both sides.
			if name == "" || objectID.MatchString(name) || st.word(kind.Name, path, name, false) ||
				(set != nil && set.Declares(target, name)) {
				return name
			}

			if failure == nil {
				failure = fmt.Errorf("%w: %s: no %s named %q", ErrUnresolved, path, target, name)
			}

			return name
		})
	}

	if failure != nil {
		return nil, failure
	}

	return out, nil
}

// rewrite applies mapping to every string at path in doc: a key, or
// list.key for a key inside a merged list's entries, holding one id or a
// list of them. Entries and lists are copied, so the document read stays
// as it was.
func rewrite(doc map[string]any, path string, mapping func(string) string) {
	key, rest, nested := strings.Cut(path, ".")

	value, ok := doc[key]
	if !ok {
		return
	}

	if nested {
		// Through a list of entries, or through one map; anything else,
		// null say, stays as it is.
		switch typed := value.(type) {
		case []any:
			doc[key] = rewriteEntries(typed, rest, mapping)
		case map[string]any:
			next := maps.Clone(typed)
			rewrite(next, rest, mapping)
			doc[key] = next
		}

		return
	}

	switch typed := value.(type) {
	case string:
		doc[key] = mapping(typed)
	case []any:
		copied := make([]any, 0, len(typed))

		for _, item := range typed {
			if s, isString := item.(string); isString {
				item = mapping(s)
			}

			copied = append(copied, item)
		}

		doc[key] = copied
	}
}

func rewriteEntries(list []any, path string, mapping func(string) string) []any {
	copied := make([]any, 0, len(list))

	for _, item := range list {
		if entry, isMap := item.(map[string]any); isMap {
			next := maps.Clone(entry)
			rewrite(next, path, mapping)

			item = next
		}

		copied = append(copied, item)
	}

	return copied
}
