package documents

import (
	"fmt"

	"github.com/duncanite/go-unifi/console"
)

// Written is what WriteSet did for one kind: how many documents it wrote,
// and the comments it had nowhere to put.
type Written struct {
	Kind    string
	Count   int
	Orphans []string
}

// WriteSet writes a dumped set under dir, one directory per kind in the
// order of kinds; a kind the set has no documents for is skipped. Each
// kind goes through Write, so its directory ends up holding the set's
// documents and no other, with the comments of the files already there
// carried over.
func WriteSet(dir string, kinds []console.Kind, set *Set) ([]Written, error) {
	var written []Written

	for _, kind := range kinds {
		docs, ok := set.Kinds[kind.Name]
		if !ok {
			continue
		}

		orphans, err := Write(dir, kind, docs)
		if err != nil {
			return nil, fmt.Errorf("writing %s: %w", kind.Name, err)
		}

		written = append(written, Written{Kind: kind.Name, Count: len(docs), Orphans: orphans})
	}

	return written, nil
}
