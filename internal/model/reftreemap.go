package model

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// RefTreeMap is `Repo.refTrees` with its key order preserved.
//
// # Why this is not a plain map
//
// Go's encoding/json sorts map keys; JSON.stringify emits them in insertion order. For three of
// the artifact's four maps those are the same thing — commits, extraCommits and files are all
// inserted in sorted order upstream, which is why they are still plain maps.
//
// refTrees is the exception, and it is invisible in the two corpora this project tests against.
// The TypeScript scanner inserts every treed BRANCH first, by name, and then every treed TAG, by
// name (src/lib/ingest/scan.ts:226-246). So a repository with a branch `other` and a tag `aaa`
// emits ["other","aaa"], while a Go map emits ["aaa","other"]. Both this repository and the e2e
// fixture happen to hold ref names whose branches-then-tags order is ALSO alphabetical, so both
// byte-compare clean while the divergence sits there waiting for the first repo with a tag that
// sorts before a branch — which is most repositories with a `v*` tag and a feature branch.
//
// Preserving order rather than reproducing the sort is deliberate: the sort is a property of how
// the scanner happens to build the map today, and encoding that here would silently break the
// day the scanner changed. Insertion order is the actual contract.
type RefTreeMap struct {
	keys []string
	m    map[string]RefTree
}

// NewRefTreeMap returns an empty map ready to Set.
func NewRefTreeMap() *RefTreeMap {
	return &RefTreeMap{m: map[string]RefTree{}}
}

// Set inserts or replaces a ref tree, remembering first-insertion order.
func (r *RefTreeMap) Set(name string, tree RefTree) {
	if r.m == nil {
		r.m = map[string]RefTree{}
	}
	if _, exists := r.m[name]; !exists {
		r.keys = append(r.keys, name)
	}
	r.m[name] = tree
}

// Get returns the ref tree for a name.
func (r *RefTreeMap) Get(name string) (RefTree, bool) {
	if r == nil || r.m == nil {
		return RefTree{}, false
	}
	t, ok := r.m[name]
	return t, ok
}

// Len is how many refs have trees.
func (r *RefTreeMap) Len() int {
	if r == nil {
		return 0
	}
	return len(r.keys)
}

// Keys returns the ref names in artifact order. The slice is a copy: a caller sorting it must
// not reorder the artifact.
func (r *RefTreeMap) Keys() []string {
	if r == nil {
		return nil
	}
	out := make([]string, len(r.keys))
	copy(out, r.keys)
	return out
}

// All iterates the ref trees in artifact order.
func (r *RefTreeMap) All() []RefTree {
	if r == nil {
		return nil
	}
	out := make([]RefTree, 0, len(r.keys))
	for _, k := range r.keys {
		out = append(out, r.m[k])
	}
	return out
}

// MarshalJSON emits the entries in insertion order.
func (r RefTreeMap) MarshalJSON() ([]byte, error) {
	if len(r.keys) == 0 {
		return []byte("{}"), nil
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range r.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := marshalNoEscape(k)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		val, err := marshalNoEscape(r.m[k])
		if err != nil {
			return nil, fmt.Errorf("refTrees[%s]: %w", k, err)
		}
		buf.Write(val)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// UnmarshalJSON records the order the keys arrived in, so a decode/encode round trip is
// byte-stable — which is what `frznforge verify` checks.
func (r *RefTreeMap) UnmarshalJSON(data []byte) error {
	r.keys = nil
	r.m = map[string]RefTree{}
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("refTrees: expected an object, got %v", tok)
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("refTrees: non-string key %v", keyTok)
		}
		var tree RefTree
		if err := dec.Decode(&tree); err != nil {
			return fmt.Errorf("refTrees[%s]: %w", key, err)
		}
		r.Set(key, tree)
	}
	_, err = dec.Token() // closing brace
	return err
}

// marshalNoEscape encodes a value with HTML escaping off, matching Serialize. Using
// json.Marshal here would re-introduce the \u003c escapes on any path or content inside a ref
// tree — the exact trap the package comment warns about, one level down.
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
