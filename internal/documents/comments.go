package documents

import (
	"fmt"
	"strconv"

	"go.yaml.in/yaml/v4"
)

// carry moves the comments of an existing file's node onto the node about
// to replace it, so a dump does not take a reader's notes away with the
// bytes. A mapping's keys are matched by name, a merged list's entries by
// the key the list is matched by, any other list by position. A head or
// foot comment sits on a key (or on a list entry), a line comment on a
// value; each goes where it was. A comment whose key the new document no
// longer has is reported by path, since there is nowhere to put it.
func carry(dst, src *yaml.Node, lists map[string]string, path string) []string {
	if dst == nil || src == nil || dst.Kind != src.Kind {
		return commented(src, path)
	}

	switch dst.Kind {
	case yaml.DocumentNode:
		dst.HeadComment = src.HeadComment
		dst.FootComment = src.FootComment

		if len(dst.Content) == 1 && len(src.Content) == 1 {
			return carry(dst.Content[0], src.Content[0], lists, path)
		}
	case yaml.MappingNode:
		return carryMapping(dst, src, lists, path)
	case yaml.SequenceNode:
		return carrySequence(dst, src, "", path)
	case yaml.ScalarNode, yaml.AliasNode:
		dst.LineComment = src.LineComment
	case yaml.StreamNode:
	}

	return nil
}

func carryMapping(dst, src *yaml.Node, lists map[string]string, path string) []string {
	var orphans []string

	placed := make(map[string]bool, len(src.Content)/2)

	last := len(src.Content) - 2

	for i := 0; i+1 < len(dst.Content); i += 2 {
		key, value := dst.Content[i], dst.Content[i+1]

		index := indexOf(src, key.Value)
		if index < 0 {
			continue
		}

		placed[key.Value] = true
		oldKey, oldValue := src.Content[index], src.Content[index+1]

		key.HeadComment = oldKey.HeadComment
		key.LineComment = oldKey.LineComment

		if index != last {
			key.FootComment = oldKey.FootComment
		}

		// A merged list's entries are matched by their key; the match key
		// is the list's at the top of a document and nowhere below.
		if value.Kind == yaml.SequenceNode && oldValue.Kind == yaml.SequenceNode {
			orphans = append(orphans, carrySequence(value, oldValue, lists[key.Value], join(path, key.Value))...)

			continue
		}

		orphans = append(orphans, carry(value, oldValue, nil, join(path, key.Value))...)
	}

	// The foot of the last key is the mapping's own foot, a file's closing
	// line: it follows whichever key is last now, matched or not.
	if last >= 0 && len(dst.Content) >= 2 {
		dst.Content[len(dst.Content)-2].FootComment = src.Content[last].FootComment
		src.Content[last].FootComment = ""
	}

	for i := 0; i+1 < len(src.Content); i += 2 {
		if key := src.Content[i]; !placed[key.Value] {
			orphans = append(orphans, commentedPair(key, src.Content[i+1], join(path, key.Value))...)
		}
	}

	return orphans
}

func carrySequence(dst, src *yaml.Node, match, path string) []string {
	var orphans []string

	placed := make(map[int]bool, len(src.Content))

	for i, item := range dst.Content {
		index := i

		if match != "" {
			index = entryIndex(src, match, entryKey(item, match))
		}

		if index < 0 || index >= len(src.Content) {
			continue
		}

		placed[index] = true
		old := src.Content[index]

		item.HeadComment = old.HeadComment
		item.LineComment = old.LineComment
		item.FootComment = old.FootComment

		orphans = append(orphans, carry(item, old, nil, label(path, item, match, i))...)
	}

	for i, old := range src.Content {
		if !placed[i] {
			orphans = append(orphans, commented(old, label(path, old, match, i))...)
		}
	}

	return orphans
}

// indexOf is the position of key in a mapping's content, or -1.
func indexOf(mapping *yaml.Node, key string) int {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return i
		}
	}

	return -1
}

// entryKey is the value a list entry carries under match, or "" when it
// is not a mapping that has one.
func entryKey(entry *yaml.Node, match string) string {
	if entry.Kind != yaml.MappingNode {
		return ""
	}

	if at := indexOf(entry, match); at >= 0 {
		return entry.Content[at+1].Value
	}

	return ""
}

// entryIndex is the position of the entry whose match key has value, or -1.
func entryIndex(list *yaml.Node, match, value string) int {
	if value == "" {
		return -1
	}

	for i, item := range list.Content {
		if entryKey(item, match) == value {
			return i
		}
	}

	return -1
}

// commented reports the path when the node, or anything under it, carries
// a comment: the one place a comment can be lost is a key that is gone.
func commented(node *yaml.Node, path string) []string {
	if node == nil {
		return nil
	}

	if node.HeadComment != "" || node.LineComment != "" || node.FootComment != "" {
		return []string{path}
	}

	for _, child := range node.Content {
		if len(commented(child, path)) > 0 {
			return []string{path}
		}
	}

	return nil
}

func commentedPair(key, value *yaml.Node, path string) []string {
	if len(commented(key, path)) > 0 {
		return []string{path}
	}

	return commented(value, path)
}

func join(path, key string) string {
	if path == "" {
		return key
	}

	return path + "." + key
}

// label names a list entry in a path: by its match key when it has one,
// by position otherwise.
func label(path string, item *yaml.Node, match string, index int) string {
	if key := entryKey(item, match); key != "" {
		return fmt.Sprintf("%s[%s]", path, key)
	}

	return path + "[" + strconv.Itoa(index) + "]"
}
