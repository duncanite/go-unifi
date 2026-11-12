// Package reconcile turns the files into what the console holds. Dump
// writes the console out as files; Plan reads both and lists every key
// that differs, file against console; Apply writes the files' keys over
// the console's documents, kind by kind in the order their references
// require, and deletes nothing.
package reconcile

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/mycophonic/primordium/fault"

	"github.com/duncanite/go-unifi/internal/console"
	"github.com/duncanite/go-unifi/internal/documents"
)

// ErrUnresolved is the sentinel for a reference the console cannot answer.
var ErrUnresolved = fault.ErrNotFound

// ErrKept is the sentinel for a change the console answered ok to and did
// not make: what it stored differs from what was sent.
var ErrKept = fault.ErrUnacceptableResponse

// objectID is how the controller spells an id: a Mongo ObjectId. A
// reference that is one already is passed through, not resolved.
var objectID = regexp.MustCompile(`^[0-9a-f]{24}$`)

// Plan is every change one apply would make, and what it leaves alone.
type Plan struct {
	Changes []Change
	// Unmanaged is every document the console holds that no file states,
	// listed for the reader and never touched.
	Unmanaged []Ref

	snapshot *state
}

// Ref names a document: its kind, and its identity in that kind.
type Ref struct {
	Kind     string
	Identity string
}

// Change is one document to create or update, with every key that differs.
type Change struct {
	Ref

	Create bool
	Diffs  []Diff

	file console.Document
	id   string
}

// Diff is one key whose value the file and the console disagree on. Old is
// the console's value, nil on a create or a new list entry.
type Diff struct {
	Path string
	Old  any
	New  any
}

// Empty reports whether the plan changes nothing.
func (p *Plan) Empty() bool {
	return len(p.Changes) == 0
}

// Write prints the plan for a reader.
func (p *Plan) Write(out io.Writer) {
	for _, change := range p.Changes {
		change.write(out)
	}

	if len(p.Unmanaged) > 0 {
		names := make([]string, 0, len(p.Unmanaged))
		for _, ref := range p.Unmanaged {
			names = append(names, ref.String())
		}

		fmt.Fprintf(out, "unmanaged, left alone: %s\n", strings.Join(names, ", "))
	}

	switch len(p.Changes) {
	case 0:
		fmt.Fprintln(out, "No changes.")
	case 1:
		fmt.Fprintln(out, "1 change.")
	default:
		fmt.Fprintf(out, "%d changes.\n", len(p.Changes))
	}
}

// changes returns the kind's changes, creates first: a document created in
// this apply may be referenced by an update that follows.
func (p *Plan) changes(kind console.Kind) []*Change {
	var ordered []*Change

	for _, create := range []bool{true, false} {
		for i := range p.Changes {
			if p.Changes[i].Kind == kind.Name && p.Changes[i].Create == create {
				ordered = append(ordered, &p.Changes[i])
			}
		}
	}

	return ordered
}

func (r Ref) String() string {
	return fmt.Sprintf("%s %q", r.Kind, r.Identity)
}

func (c *Change) write(out io.Writer) {
	if c.Create {
		fmt.Fprintf(out, "%s (create)\n", c.Ref)
	} else {
		fmt.Fprintf(out, "%s\n", c.Ref)
	}

	for _, diff := range c.Diffs {
		switch {
		case c.Create, diff.Old == nil:
			fmt.Fprintf(out, "  + %s: %s\n", diff.Path, render(diff.New))
		default:
			fmt.Fprintf(out, "  ~ %s: %s -> %s\n", diff.Path, render(diff.Old), render(diff.New))
		}
	}
}

func render(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}

	return string(encoded)
}

// Reconciler holds the console.
type Reconciler struct {
	Console *console.Client
}

// Dump returns every managed document the console holds, as files would
// state it: the controller's keys dropped, references by name.
func (r *Reconciler) Dump(ctx context.Context) (*documents.Set, error) {
	snapshot, err := r.fetch(ctx)
	if err != nil {
		return nil, err
	}

	set := &documents.Set{Kinds: make(map[string][]console.Document)}

	for _, kind := range console.Kinds() {
		if !kind.Managed() {
			continue
		}

		docs := make([]console.Document, 0, len(snapshot.current[kind.Name]))

		for _, doc := range snapshot.current[kind.Name] {
			if exported, ok := snapshot.file(kind, doc); ok {
				docs = append(docs, exported)
			}
		}

		set.Kinds[kind.Name] = docs
	}

	return set, nil
}

// Plan computes the changes that would make the console match the set.
func (r *Reconciler) Plan(ctx context.Context, set *documents.Set) (*Plan, error) {
	snapshot, err := r.fetch(ctx)
	if err != nil {
		return nil, err
	}

	plan := &Plan{snapshot: snapshot}

	for _, kind := range console.Kinds() {
		if !kind.Managed() {
			continue
		}

		if err := plan.planKind(kind, set); err != nil {
			return nil, err
		}
	}

	return plan, nil
}

func (p *Plan) planKind(kind console.Kind, set *documents.Set) error {
	stated := make(map[string]struct{}, len(set.Kinds[kind.Name]))

	for _, file := range set.Kinds[kind.Name] {
		identity := documents.Identity(kind, file)
		stated[identity] = struct{}{}

		change, err := p.planDocument(kind, file, set)
		if err != nil {
			return fmt.Errorf("%s %q: %w", kind.Name, identity, err)
		}

		if change != nil {
			p.Changes = append(p.Changes, *change)
		}
	}

	for _, current := range p.snapshot.current[kind.Name] {
		identity := p.snapshot.identity(kind, current)
		if _, ok := stated[identity]; ok {
			continue
		}

		// What no file could state is not unmanaged, it is not a file's.
		if _, isFile := p.snapshot.file(kind, current); isFile {
			p.Unmanaged = append(p.Unmanaged, Ref{Kind: kind.Name, Identity: identity})
		}
	}

	return nil
}

// planDocument returns the change one file asks for, or nil when the
// console already agrees with it.
func (p *Plan) planDocument(kind console.Kind, file console.Document, set *documents.Set) (*Change, error) {
	identity := documents.Identity(kind, file)
	ref := Ref{Kind: kind.Name, Identity: identity}

	desired, err := p.snapshot.resolve(kind, file, set)
	if err != nil {
		return nil, err
	}

	current, found := p.snapshot.find(kind, identity)
	if !found {
		if kind.Name == console.Device {
			return nil, fmt.Errorf("%w: not adopted; the console adopts, the file configures", ErrUnresolved)
		}

		return &Change{Ref: ref, Create: true, Diffs: p.snapshot.named(kind, creation(desired)), file: file}, nil
	}

	diffs := diff(kind, desired, current)
	if len(diffs) == 0 {
		return nil, nil //nolint:nilnil // no change is the answer, not a failure
	}

	return &Change{Ref: ref, Diffs: p.snapshot.named(kind, diffs), file: file, id: kind.ID(current)}, nil
}

// Apply makes the changes, kinds in the order Kinds lists them, creates
// before updates within a kind so a reference to a document created in the
// same apply resolves before it is written.
func (r *Reconciler) Apply(ctx context.Context, plan *Plan) error {
	for _, kind := range console.Kinds() {
		changes := plan.changes(kind)

		for _, change := range changes {
			if err := r.write(ctx, kind, change, plan.snapshot); err != nil {
				return fmt.Errorf("%s: %w", change.Ref, err)
			}
		}

		if kind.Rank != nil {
			if err := r.rank(ctx, kind, changes, plan.snapshot); err != nil {
				return fmt.Errorf("%s: %w", kind.Name, err)
			}
		}
	}

	return nil
}

// write sends one change. On a ranked kind the rank is not in what is
// sent: the controller takes it from the batch rank sends after the
// kind's writes, not from the document; a change that is the rank alone
// sends nothing here.
func (r *Reconciler) write(ctx context.Context, kind console.Kind, change *Change, snapshot *state) error {
	desired, err := snapshot.resolve(kind, change.file, nil)
	if err != nil {
		return err
	}

	desired = unranked(kind, desired)

	if change.Create {
		return r.create(ctx, kind, desired, snapshot)
	}

	current, found := snapshot.find(kind, change.Identity)
	if !found {
		return fmt.Errorf("%w: gone from the console since the plan", fault.ErrNotFound)
	}

	if len(diff(kind, desired, current)) == 0 {
		return nil
	}

	stored, err := r.Console.Update(ctx, kind, change.id, merge(kind, current, desired))
	if err != nil {
		return fmt.Errorf("updating: %w", err)
	}

	return kept(kind, desired, stored)
}

// unranked is doc without the rank, on a ranked kind.
func unranked(kind console.Kind, doc console.Document) console.Document {
	if kind.Rank == nil {
		return doc
	}

	out := maps.Clone(doc)
	delete(out, kind.Rank.Key)

	return out
}

// rank sends the controller the order of every group a change moved a
// document in, as the files state it, and checks the numbers it handed
// back. A create lands where the controller's count is, whatever the
// file says, and an update does not move a document, so a file's rank is
// made true here or not at all. A group's documents no file ranks keep
// their place among the others.
func (r *Reconciler) rank(ctx context.Context, kind console.Kind, changes []*Change, snapshot *state) error {
	wanted := make(map[string]int64)
	groups := make(map[string]map[string]string)

	for _, change := range changes {
		rank, stated := rankOf(change.file[kind.Rank.Key])
		if !stated || !change.moved(kind) {
			continue
		}

		current, found := snapshot.find(kind, change.Identity)
		if !found {
			return fmt.Errorf("%s: %w: gone from the console since the plan", change.Ref, fault.ErrNotFound)
		}

		wanted[change.Identity] = rank

		group := groupOf(kind, current)
		groups[render(group)] = group
	}

	for _, key := range slices.Sorted(maps.Keys(groups)) {
		if err := r.rankGroup(ctx, kind, groups[key], wanted, snapshot); err != nil {
			return err
		}
	}

	return nil
}

// moved reports whether the change creates the document or changes its
// rank.
func (c *Change) moved(kind console.Kind) bool {
	if c.Create {
		return true
	}

	for _, d := range c.Diffs {
		if d.Path == kind.Rank.Key {
			return true
		}
	}

	return false
}

// member is one document of a group in the order the files ask for: its
// id, the rank it is to hold, and whether it is the controller's own.
type member struct {
	id       string
	identity string
	rank     int64
	own      bool
}

// rankGroup orders one group: every document of the group the console
// holds, at the rank its file states or, with no file moving it, the rank
// it has, the controller's own left to the controller, and the rest sent
// in that order, before and after the controller's own. The answer is
// read back: a document numbered other than its file says is kept.
func (r *Reconciler) rankGroup(
	ctx context.Context, kind console.Kind, group map[string]string, wanted map[string]int64, snapshot *state,
) error {
	members := make([]member, 0)
	boundary := int64(-1)

	for _, doc := range snapshot.current[kind.Name] {
		if !maps.Equal(groupOf(kind, doc), group) {
			continue
		}

		identity := snapshot.identity(kind, doc)
		rank, _ := rankOf(doc[kind.Rank.Key])

		if want, moved := wanted[identity]; moved {
			rank = want
		}

		own := doc[kind.Rank.Own] == true
		if own && (boundary < 0 || rank < boundary) {
			boundary = rank
		}

		members = append(members, member{id: kind.ID(doc), identity: identity, rank: rank, own: own})
	}

	slices.SortStableFunc(members, func(a, b member) int { return cmp.Compare(a.rank, b.rank) })

	order := console.Order{Group: group}

	for _, item := range members {
		switch {
		case item.own:
		case boundary < 0 || item.rank < boundary:
			order.Before = append(order.Before, item.id)
		default:
			order.After = append(order.After, item.id)
		}
	}

	numbered, err := r.Console.Rank(ctx, kind, order)
	if err != nil {
		return fmt.Errorf("ranking: %w", err)
	}

	return renumbered(kind, members, wanted, numbered, snapshot)
}

// renumbered reads the ranks the controller handed back into the
// snapshot and names every document numbered other than its file says.
func renumbered(
	kind console.Kind, members []member, wanted map[string]int64, numbered []console.Document, snapshot *state,
) error {
	if numbered == nil {
		return nil
	}

	given := make(map[string]int64, len(numbered))

	for _, doc := range numbered {
		canonical, err := documents.Canonical(doc)
		if err != nil {
			return fmt.Errorf("the console's answer: %w", err)
		}

		if rank, ok := rankOf(canonical[kind.Rank.Key]); ok {
			given[kind.ID(canonical)] = rank
		}
	}

	var lines []string

	for _, item := range members {
		rank, answered := given[item.id]
		if !answered {
			continue
		}

		snapshot.rerank(kind, item.id, rank)

		if want, moved := wanted[item.identity]; moved && want != rank {
			lines = append(lines, fmt.Sprintf("%q: %s %d, numbered %d", item.identity, kind.Rank.Key, want, rank))
		}
	}

	if len(lines) == 0 {
		return nil
	}

	return fmt.Errorf("%w: the console ranked the policies its own way: %s", ErrKept, strings.Join(lines, "; "))
}

// groupOf names the group doc belongs to, by the batch's keys.
func groupOf(kind console.Kind, doc console.Document) map[string]string {
	group := make(map[string]string, len(kind.Rank.Group))

	for key, path := range kind.Rank.Group {
		value, _ := documents.At(doc, path)
		group[key] = value
	}

	return group
}

// rankOf is value as a rank, when it is a whole number.
func rankOf(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case float64:
		return int64(typed), typed == float64(int64(typed))
	default:
		return 0, false
	}
}

// kept compares what the console stored with what the file asked for and
// names every key the console kept its own value of; an apply that the
// console answered ok to and did not make is not applied. A console that
// answered nothing cannot be checked, and is trusted.
func kept(kind console.Kind, desired, stored console.Document) error {
	if stored == nil {
		return nil
	}

	canonical, err := documents.Canonical(stored)
	if err != nil {
		return fmt.Errorf("the console's answer: %w", err)
	}

	diffs := diff(kind, desired, canonical)
	if len(diffs) == 0 {
		return nil
	}

	lines := make([]string, 0, len(diffs))
	for _, d := range diffs {
		lines = append(lines, fmt.Sprintf("%s: sent %s, kept %s", d.Path, render(d.New), render(d.Old)))
	}

	return fmt.Errorf("%w: the console answered ok and kept its own values: %s", ErrKept, strings.Join(lines, "; "))
}

// create adds the document to the console and to the snapshot, so a
// reference to it later in the same apply resolves. A kind created by
// writing, a setting, is written where its update goes.
func (r *Reconciler) create(ctx context.Context, kind console.Kind, desired console.Document, snapshot *state) error {
	var (
		created console.Document
		err     error
	)

	if kind.CreatesByUpdate() {
		created, err = r.Console.Update(ctx, kind, kind.ID(desired), desired)
	} else {
		created, err = r.Console.Create(ctx, kind, desired)
	}

	if err != nil {
		return fmt.Errorf("creating: %w", err)
	}

	// The controller's answer is read like an update's: what it stored
	// with its own values in place of the file's is not created as asked.
	// A controller that answered nothing is trusted, and the file stands
	// for what it holds.
	if keptErr := kept(kind, desired, created); keptErr != nil {
		return keptErr
	}

	if created == nil {
		created = desired
	}

	canonical, err := documents.Canonical(created)
	if err != nil {
		return fmt.Errorf("created %s: %w", kind.Name, err)
	}

	snapshot.add(kind, canonical)

	return nil
}

// creation lists every key of a new document as a diff from nothing.
func creation(doc console.Document) []Diff {
	diffs := make([]Diff, 0, len(doc))
	for _, key := range slices.Sorted(maps.Keys(doc)) {
		diffs = append(diffs, Diff{Path: key, New: doc[key]})
	}

	return diffs
}

// diff lists every key of desired whose value current lacks or differs on;
// a merged list is compared entry by entry on the keys the file states,
// each the way a top-level key is.
func diff(kind console.Kind, desired, current console.Document) []Diff {
	var diffs []Diff

	for _, key := range slices.Sorted(maps.Keys(desired)) {
		want := desired[key]

		if match, merged := kind.Lists[key]; merged {
			diffs = append(diffs, diffList(key, match, asList(want), asList(current[key]))...)

			continue
		}

		diffs = append(diffs, diffValue(key, want, current[key])...)
	}

	return diffs
}

// diffValue compares one value at path. A map is a partial document like
// the file itself: the keys it states are compared, one by one, and the
// keys it leaves out are the controller's; anything else is compared whole.
func diffValue(path string, want, have any) []Diff {
	wantMap, wantIsMap := want.(map[string]any)
	haveMap, haveIsMap := have.(map[string]any)

	if !wantIsMap || !haveIsMap {
		if !reflect.DeepEqual(have, want) {
			return []Diff{{Path: path, Old: have, New: want}}
		}

		return nil
	}

	var diffs []Diff

	for _, key := range slices.Sorted(maps.Keys(wantMap)) {
		diffs = append(diffs, diffValue(path+"."+key, wantMap[key], haveMap[key])...)
	}

	return diffs
}

func diffList(key, match string, want, have []any) []Diff {
	var diffs []Diff

	for _, item := range want {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}

		label := fmt.Sprintf("%s[%s]", key, render(entry[match]))

		existing := findEntry(have, match, entry[match])
		if existing == nil {
			diffs = append(diffs, Diff{Path: label, New: entry})

			continue
		}

		for _, field := range slices.Sorted(maps.Keys(entry)) {
			diffs = append(diffs, diffValue(label+"."+field, entry[field], existing[field])...)
		}
	}

	return diffs
}

// merge returns current with desired laid over it: every stated key
// replaced, every merged list's entries overlaid or appended, and, for a
// kind with a fixed key set, only those keys, so a device's runtime state
// is not sent back to it.
func merge(kind console.Kind, current, desired console.Document) console.Document {
	out := make(console.Document, len(current)+len(desired))

	for key, value := range current {
		if kind.Keys == nil || slices.Contains(kind.Keys, key) || console.Owned(key) {
			out[key] = value
		}
	}

	for key, want := range desired {
		if match, merged := kind.Lists[key]; merged {
			out[key] = mergeList(match, asList(want), asList(current[key]))

			continue
		}

		out[key] = mergeValue(want, current[key])
	}

	return out
}

// mergeValue lays want over have: a map key by key, down through maps,
// so a file's partial source keeps the controller's other keys; anything
// else is replaced.
func mergeValue(want, have any) any {
	wantMap, wantIsMap := want.(map[string]any)
	haveMap, haveIsMap := have.(map[string]any)

	if !wantIsMap || !haveIsMap {
		return want
	}

	out := maps.Clone(haveMap)
	for key, value := range wantMap {
		out[key] = mergeValue(value, haveMap[key])
	}

	return out
}

func mergeList(match string, want, have []any) []any {
	out := make([]any, 0, len(have)+len(want))

	for _, item := range have {
		if entry, ok := item.(map[string]any); ok {
			out = append(out, maps.Clone(entry))

			continue
		}

		out = append(out, item)
	}

	for _, item := range want {
		entry, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)

			continue
		}

		if existing := findEntry(out, match, entry[match]); existing != nil {
			for field, value := range entry {
				existing[field] = mergeValue(value, existing[field])
			}

			continue
		}

		out = append(out, entry)
	}

	return out
}

func findEntry(list []any, match string, value any) map[string]any {
	for _, item := range list {
		if entry, ok := item.(map[string]any); ok && reflect.DeepEqual(entry[match], value) {
			return entry
		}
	}

	return nil
}

func asList(value any) []any {
	list, ok := value.([]any)
	if !ok {
		return nil
	}

	return list
}
