// Package documents reads and writes the console's documents as files: one
// directory per kind, one YAML file per document, the controller's keys as
// the controller spells them. A file states what it enforces and nothing
// else; a key the controller owns, or hides as a secret, is refused.
package documents

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/forkcloser/xos"
	"github.com/mycophonic/primordium/fault"
	"github.com/mycophonic/primordium/filesystem"
	"go.yaml.in/yaml/v4"

	"github.com/duncanite/go-unifi/internal/console"
)

// Extension is the one the files carry.
const Extension = ".yaml"

// ErrInvalidDocument is the sentinel every refused file wraps.
var ErrInvalidDocument = fault.ErrInvalidArgument

// Set is every document the directory holds, by kind, in file order.
type Set struct {
	Kinds map[string][]console.Document
}

// Load reads every kind's directory under dir. A kind without a directory
// has no documents; a file that states a key the controller owns, a secret,
// or no identity, or two files with one identity, fail the load.
func Load(dir string, kinds []console.Kind) (*Set, error) {
	set := &Set{Kinds: make(map[string][]console.Document, len(kinds))}

	for _, kind := range kinds {
		docs, err := loadKind(filepath.Join(dir, kind.Name), kind)
		if err != nil {
			return nil, err
		}

		if docs != nil {
			set.Kinds[kind.Name] = docs
		}
	}

	return set, nil
}

func loadKind(dir string, kind console.Kind) ([]console.Document, error) {
	paths, err := list(dir)
	if err != nil {
		return nil, err
	}

	if len(paths) == 0 {
		return nil, nil
	}

	docs := make([]console.Document, 0, len(paths))
	seen := make(map[string]string, len(paths))

	for _, path := range paths {
		doc, err := loadFile(path, kind)
		if err != nil {
			return nil, err
		}

		identity := Identity(kind, doc)
		if other, dup := seen[identity]; dup {
			return nil, fmt.Errorf(
				"%w: %s and %s both declare %s %q",
				ErrInvalidDocument,
				other,
				path,
				kind.Name,
				identity,
			)
		}

		seen[identity] = path

		docs = append(docs, doc)
	}

	return docs, nil
}

// list is every document file in the directory, sorted; a directory that
// is not there holds none. A file that is not a document is refused rather
// than skipped, so a .yml is not silently nothing.
func list(dir string) ([]string, error) {
	paths, others, err := files(dir)
	if err != nil {
		return nil, err
	}

	if len(others) > 0 {
		return nil, fmt.Errorf(
			"%w: %s is not a document: a document's file ends in %s",
			ErrInvalidDocument,
			others[0],
			Extension,
		)
	}

	return paths, nil
}

// files sorts a directory's entries into document files and the other
// files; directories and dotfiles are neither. A directory that is not
// there holds nothing.
func files(dir string) (documents, others []string, err error) {
	entries, err := xos.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}

	if err != nil {
		return nil, nil, fmt.Errorf("%w: listing %s: %w", fault.ErrReadFailure, dir, err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}

		if strings.HasSuffix(name, Extension) {
			documents = append(documents, filepath.Join(dir, name))
		} else {
			others = append(others, filepath.Join(dir, name))
		}
	}

	slices.Sort(documents)
	slices.Sort(others)

	return documents, others, nil
}

// Empty reports whether dir holds nothing a dump would write over or read:
// no files but dotfiles, or no directory at all. A kind's directory inside
// it counts, empty or not, since a dump replaces its contents.
func Empty(dir string) (bool, error) {
	entries, err := xos.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}

	if err != nil {
		return false, fmt.Errorf("%w: listing %s: %w", fault.ErrReadFailure, dir, err)
	}

	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".") {
			return false, nil
		}
	}

	return true, nil
}

func loadFile(path string, kind console.Kind) (console.Document, error) {
	data, err := xos.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: reading %s: %w", fault.ErrReadFailure, path, err)
	}

	var raw map[string]any

	dec := yaml.NewDecoder(bytes.NewReader(data))
	if decodeErr := dec.Decode(&raw); decodeErr != nil {
		return nil, fmt.Errorf("%w: parsing %s: %w", ErrInvalidDocument, path, decodeErr)
	}

	doc, err := Canonical(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalidDocument, path, err)
	}

	if err := validate(kind, doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	return doc, nil
}

func validate(kind console.Kind, doc console.Document) error {
	identity := Identity(kind, doc)
	if identity == "" {
		return fmt.Errorf(
			"%w: no identity: a %s is named by %q%s",
			ErrInvalidDocument,
			kind.Name,
			kind.Identity,
			qualifiers(kind),
		)
	}

	if kind.IsOwn(doc) {
		return fmt.Errorf(
			"%w: %s %q is the console's own, generated there, not a file's to state",
			ErrInvalidDocument,
			kind.Name,
			identity,
		)
	}

	if kind.Derived(doc) {
		return fmt.Errorf(
			"%w: %s %q is derived from another document (%s); state that one",
			ErrInvalidDocument,
			kind.Name,
			identity,
			kind.DerivedBy,
		)
	}

	for key, value := range doc {
		if err := validateField(kind, key, value); err != nil {
			return err
		}
	}

	return nil
}

// validateField is one top-level key of a file: a key a file may state on
// this kind, holding a value with no controller's key inside it.
func validateField(kind console.Kind, key string, value any) error {
	if err := validateKey(key); err != nil {
		return err
	}

	if kind.Mirrored(key) {
		return fmt.Errorf(
			"%w: %q on a %s is the controller's view of other documents; state it on them",
			ErrInvalidDocument,
			key,
			kind.Name,
		)
	}

	if kind.Keys != nil && !slices.Contains(kind.Keys, key) {
		return fmt.Errorf("%w: %q is not a key the controller takes on a %s", ErrInvalidDocument, key, kind.Name)
	}

	if match, merged := kind.Lists[key]; merged {
		if err := validateEntries(value, match); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}

	if err := validateValue(value); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}

	return nil
}

// validateValue applies validateKey to every map inside value, however
// deep: a secret inside a policy's source is as much a secret.
func validateValue(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if err := validateKey(key); err != nil {
				return err
			}

			if err := validateValue(item); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
	case []any:
		for _, item := range typed {
			if err := validateValue(item); err != nil {
				return err
			}
		}
	}

	return nil
}

// qualifiers spells a kind's qualifier paths for a message, or nothing.
func qualifiers(kind console.Kind) string {
	if len(kind.Qualifiers) == 0 {
		return ""
	}

	return " and " + strings.Join(kind.Qualifiers, " and ")
}

// validateKey refuses a key no file states, at the top or in an entry.
func validateKey(key string) error {
	switch {
	case console.Secret(key):
		return fmt.Errorf("%w: %q is a secret: it stays on the controller, never in a file", ErrInvalidDocument, key)
	case console.Owned(key):
		return fmt.Errorf("%w: %q is the controller's own, not a file's to state", ErrInvalidDocument, key)
	}

	return nil
}

// validateEntries requires each of a merged list's entries to carry the
// key it is matched by.
func validateEntries(value any, match string) error {
	list, isList := value.([]any)
	if !isList {
		return nil
	}

	for _, item := range list {
		if entry, isMap := item.(map[string]any); isMap && entry[match] == nil {
			return fmt.Errorf("%w: an entry without %q, the key an entry is matched by", ErrInvalidDocument, match)
		}
	}

	return nil
}

// Identity returns the value of the kind's identity key, or "" when the
// document has none.
// A kind with qualifiers names a document by its identity key and the
// values at the qualifier paths, "name (first to second)": all must be
// there, or the document has no identity.
func Identity(kind console.Kind, doc console.Document) string {
	identity, ok := doc[kind.Identity].(string)
	if !ok || identity == "" {
		return ""
	}

	if len(kind.Qualifiers) == 0 {
		return identity
	}

	parts := make([]string, 0, len(kind.Qualifiers))

	for _, path := range kind.Qualifiers {
		value, found := At(doc, path)
		if !found || value == "" {
			return ""
		}

		parts = append(parts, value)
	}

	return identity + " (" + strings.Join(parts, " to ") + ")"
}

// At is the string at a dotted path through maps, and whether there is one.
func At(doc map[string]any, path string) (string, bool) {
	key, rest, nested := strings.Cut(path, ".")

	value, ok := doc[key]
	if !ok {
		return "", false
	}

	if nested {
		inner, isMap := value.(map[string]any)
		if !isMap {
			return "", false
		}

		return At(inner, rest)
	}

	text, isString := value.(string)

	return text, isString
}

// Write makes dir/<kind> hold these documents and no other: one file per
// document, the identity key first and the rest sorted, and every document
// file the call did not write removed, so a document renamed or deleted on
// the console does not linger as a file that would recreate it. A file
// already there lends its comments to the one that replaces it, key by
// key; the comments whose key is gone are returned by path, "<file>: <key>",
// for the caller to say so. Two identities that spell the same file name
// are refused. A file that is not a document is not the dump's and is left
// where it is. The files are the repository's, so they take the default
// permissions.
func Write(dir string, kind console.Kind, docs []console.Document) ([]string, error) {
	target := filepath.Join(dir, kind.Name)

	if err := os.MkdirAll(target, filesystem.DirPermissionsDefault); err != nil {
		return nil, fmt.Errorf("%w: creating %s: %w", fault.ErrWriteFailure, target, err)
	}

	var orphans []string

	written := make(map[string]string, len(docs))

	for _, doc := range docs {
		identity := Identity(kind, doc)
		path := filepath.Join(target, Slug(identity)+Extension)

		if other, taken := written[path]; taken {
			return nil, fmt.Errorf(
				"%w: %s %q and %q both write to %s",
				ErrInvalidDocument,
				kind.Name,
				other,
				identity,
				path,
			)
		}

		written[path] = identity

		lost, err := writeFile(path, kind, doc)
		if err != nil {
			return nil, err
		}

		orphans = append(orphans, lost...)
	}

	stale, _, err := files(target)
	if err != nil {
		return nil, err
	}

	for _, path := range stale {
		if _, ours := written[path]; ours {
			continue
		}

		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("%w: removing %s: %w", fault.ErrWriteFailure, path, err)
		}
	}

	return orphans, nil
}

// writeFile renders the document over the file at path, the file's
// comments carried, and returns the comments that found no key as
// "<path>: <key>".
func writeFile(path string, kind console.Kind, doc console.Document) ([]string, error) {
	data, lost, err := render(kind, doc, existing(path))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	orphans := make([]string, 0, len(lost))
	for _, key := range lost {
		orphans = append(orphans, path+": "+key)
	}

	if err := filesystem.WriteFile(path, data, filesystem.FilePermissionsDefault); err != nil {
		return nil, fmt.Errorf("%w: writing %s: %w", fault.ErrWriteFailure, path, err)
	}

	return orphans, nil
}

// existing is the file at path as YAML nodes, comments included, or nil
// when there is none or it does not parse: a file that is not YAML has no
// comments to keep.
func existing(path string) *yaml.Node {
	data, err := xos.ReadFile(path)
	if err != nil {
		return nil
	}

	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil || node.Kind != yaml.DocumentNode {
		return nil
	}

	return &node
}

// Marshal renders a document as its file: the identity first, then every
// other key in order, nested maps sorted the same way.
func Marshal(kind console.Kind, doc console.Document) ([]byte, error) {
	data, _, err := render(kind, doc, nil)

	return data, err
}

// render is Marshal with the comments of an earlier rendering, when one is
// given, carried onto the new one; the second result names the comments
// that found no key.
func render(kind console.Kind, doc console.Document, old *yaml.Node) ([]byte, []string, error) {
	mapping, err := mapNode(doc, kind.Identity)
	if err != nil {
		return nil, nil, err
	}

	// The document node carries the comments that sit on no key: a block
	// at the top of the file set apart by a blank line, a closing one.
	node := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{mapping}}

	var orphans []string

	if old != nil {
		orphans = carry(node, old, kind.Lists, "")
	}

	var buf bytes.Buffer

	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)

	if err := enc.Encode(node); err != nil {
		return nil, nil, fmt.Errorf(
			"%w: encoding %s %q: %w",
			fault.ErrWriteFailure,
			kind.Name,
			Identity(kind, doc),
			err,
		)
	}

	if err := enc.Close(); err != nil {
		return nil, nil, fmt.Errorf(
			"%w: encoding %s %q: %w",
			fault.ErrWriteFailure,
			kind.Name,
			Identity(kind, doc),
			err,
		)
	}

	return buf.Bytes(), orphans, nil
}

// mapNode builds a sorted mapping node, with first, when present, ahead of
// the rest; the encoder would otherwise order the keys by its own rule.
func mapNode(doc map[string]any, first string) (*yaml.Node, error) {
	keys := make([]string, 0, len(doc))

	for key := range doc {
		if key != first {
			keys = append(keys, key)
		}
	}

	slices.Sort(keys)

	if _, ok := doc[first]; ok {
		keys = append([]string{first}, keys...)
	}

	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}

	for _, key := range keys {
		value, err := valueNode(doc[key])
		if err != nil {
			return nil, err
		}

		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
	}

	return node, nil
}

func valueNode(value any) (*yaml.Node, error) {
	switch typed := value.(type) {
	case map[string]any:
		return mapNode(typed, "")
	case []any:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}

		for _, item := range typed {
			child, err := valueNode(item)
			if err != nil {
				return nil, err
			}

			node.Content = append(node.Content, child)
		}

		return node, nil
	default:
		node := &yaml.Node{}
		if err := node.Encode(value); err != nil {
			return nil, fmt.Errorf("%w: %v: %w", fault.ErrWriteFailure, value, err)
		}

		return node, nil
	}
}

// Slug turns an identity into a file name: lowercase, runs of anything but
// letters and digits as one dash, so "Pantry Switch" and 02:00:5e:00:53:91
// both name a file.
func Slug(identity string) string {
	var builder strings.Builder

	dash := false

	for _, r := range strings.ToLower(identity) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)

			dash = false
		case !dash && builder.Len() > 0:
			builder.WriteByte('-')

			dash = true
		}
	}

	slug := strings.TrimSuffix(builder.String(), "-")
	if slug == "" {
		return "unnamed"
	}

	return slug
}

// Canonical returns doc as JSON would carry it to the controller: maps of
// string, slices of any, strings, bools, and numbers as int64 when whole
// and float64 otherwise, so a YAML 3600 and the controller's 3600 compare
// equal whichever side they came from.
func Canonical(doc map[string]any) (console.Document, error) {
	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", fault.ErrInvalidJSON, err)
	}

	dec := json.NewDecoder(bytes.NewReader(encoded))
	dec.UseNumber()

	var out console.Document
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: %w", fault.ErrInvalidJSON, err)
	}

	for key, value := range out {
		out[key] = numbers(value)
	}

	return out, nil
}

// numbers replaces every json.Number in value with an int64 or a float64.
func numbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if whole, err := typed.Int64(); err == nil {
			return whole
		}

		fraction, _ := typed.Float64()

		return fraction
	case map[string]any:
		for key, item := range typed {
			typed[key] = numbers(item)
		}

		return typed
	case []any:
		for i, item := range typed {
			typed[i] = numbers(item)
		}

		return typed
	default:
		return value
	}
}

// Declares reports whether the set holds a document of kind with that
// identity: the answer to "will this name exist once the set is applied".
func (s *Set) Declares(kind, identity string) bool {
	for _, doc := range s.Kinds[kind] {
		if name, ok := doc[identityKey(kind)].(string); ok && name == identity {
			return true
		}
	}

	return false
}

func identityKey(kind string) string {
	for _, candidate := range console.Kinds() {
		if candidate.Name == kind {
			return candidate.Identity
		}
	}

	return "name"
}
