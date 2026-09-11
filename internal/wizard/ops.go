package wizard

// The whole-config editor's vocabulary: `set`/`unset` a field, `add` to / `removeAt` from /
// `setAt` inside one of the config's lists. Ported from the operations half of
// scripts/lib/web-init.ts.
//
// Every operation is checked against the allow-lists below before it can touch the file. The
// browser proposes; this table disposes. A path not listed here cannot be written through the
// wizard, which is the property that keeps "arbitrary JSON from a local web page" from becoming
// "arbitrary edits to the file this machine builds from".
//
// Values are then judged twice: shape here (primitives and string lists, no control characters),
// and MEANING by applying the operations to a copy of the loaded config and running the result
// through the real parser — so a broken theme.heat ordering, a reserved hosting slug or a bad
// site.base is refused with config.Validate's own words before a byte of the file moves.

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// setPaths are the scalar fields the wizard may write. Kept in step with the GROUPS table in
// page.html: a field the page offers and this set omits fails the whole save with a 400.
var setPaths = map[string]bool{
	"site.title": true, "site.url": true, "site.description": true, "site.base": true,
	"owner.name": true, "owner.handle": true, "owner.profile": true, "owner.avatar": true,
	"theme.palette": true, "theme.heat.hot": true, "theme.heat.warm": true,
	"theme.heat.neutral": true, "theme.heat.cool": true,
	"markdown.mermaid":     true,
	"content.orgs":         true,
	"listing.pageSize":     true,
	"notes.dir":            true,
	"notes.useMtime":       true,
	"notes.maxFileBytes":   true,
	"hosting.maxFileBytes": true,
	"ingest.outDir":        true, "ingest.maxBlobBytes": true, "ingest.maxCommits": true,
	"ingest.maxCommitAgeDays": true, "ingest.concurrency": true, "ingest.tagTrees": true,
	"ingest.branchTrees": true, "ingest.archives": true, "ingest.cacheDir": true,
	"ingest.fetch": true, "ingest.failOnDegraded": true, "ingest.skipMetaRefetches": true,
	"ingest.reuse.enabled": true, "ingest.reuse.maxAgeMinutes": true,
	"ingest.reuse.skipUnchanged": true, "ingest.reuse.cooldownSeconds": true,
	"ingest.insights.enabled": true, "ingest.insights.samples": true,
	"ingest.insights.maxBytesPerSample": true,
}

// unsetPaths are the optional fields the page may clear.
//
// `unset` writes a literal `null`, which the loader reads exactly like an absent key. There is
// no textual "delete this line" operation, because deleting lines is how the comment beside a
// field gets destroyed.
var unsetPaths = map[string]bool{
	"site.url": true, "site.description": true, "site.base": true,
	"notes.maxFileBytes": true, "owner.avatar": true, "ingest.reuse.cooldownSeconds": true,
}

// arrayItemSpec describes one list the wizard can add to.
type arrayItemSpec struct {
	// order is every allowed field, in the order a new entry writes them. An ordered slice
	// rather than a set because the entry lands in a file a person reads: two adds of the same
	// values must produce the same bytes, and a map's iteration order would not.
	order []string
	// required names the fields an entry cannot be added without.
	required []string
	// lists names the fields whose value is a list of strings rather than a string.
	lists map[string]bool
}

var arrayAddSpecs = map[string]arrayItemSpec{
	"organizations": {
		order:    []string{"slug", "name", "description", "repos", "avatar"},
		required: []string{"slug", "name"},
		lists:    map[string]bool{"repos": true},
	},
	"hosting.sites": {
		order:    []string{"repo", "slug", "branch"},
		required: []string{"repo"},
	},
	"contributors": {
		order:    []string{"name", "emails", "description", "url", "avatar"},
		required: []string{"name"},
		lists:    map[string]bool{"emails": true},
	},
}

// arraySetFields are the fields the page may edit in place on an existing entry.
//
// Narrower than the add specs on purpose: an organization's slug is its identity and is what
// repos point at, so renaming it in place would silently orphan every member. Remove and re-add
// remains the way to change an identity.
var arraySetFields = map[string][]string{
	"organizations": {"name", "description", "avatar"},
	"hosting.sites": {"slug", "branch"},
	"contributors":  {"name", "avatar", "description", "url"},
	"repos":         {"slug", "org", "releases"},
}

// arrayRemoveKeys are the fields an `expect` may match on, per list. `repos` entries are added
// by the picker (/api/write), so `add` is deliberately absent from arrayAddSpecs for them while
// remove and edit are not.
var arrayRemoveKeys = map[string][]string{
	"organizations": {"slug", "name"},
	"hosting.sites": {"repo", "slug", "branch"},
	"contributors":  {"name"},
	"repos":         {"type", "host", "owner", "repo", "project", "path", "slug"},
}

const maxOperations = 50

// settingsOp is one validated operation. Only the fields its op uses are set.
type settingsOp struct {
	op   string
	path []string
	// value carries `set`'s new value and `setAt`'s (always a string there).
	value any
	// item is `add`'s new entry, already ordered for rendering.
	item object
	// index pins `removeAt` and `setAt` to a position rather than to a content match.
	index int
	// key is the field `setAt` writes.
	key string
	// expect is the safety net: the fields the page believes sit at index.
	expect map[string]string
}

func (o settingsOp) pathText() string { return strings.Join(o.path, ".") }

// sortedKeys is how this file walks a decoded JSON object. Go randomises map iteration, and a
// validator that refuses the FIRST bad key would otherwise name a different one each run.
func sortedKeys(raw map[string]any) []string {
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

/* ------------------------------------------------------------------ validation */

// asSettingString rejects control characters: these strings end up inside a JSON string literal
// in a file people read, and a stray newline or NUL there is never something a user typed on
// purpose.
func asSettingString(value any, label string) (string, error) {
	text, ok := value.(string)
	if !ok || len(text) > 2000 {
		return "", badRequest(fmt.Sprintf("invalid %s: expected a plain string", label))
	}
	for _, r := range text {
		if r < 0x20 || r == 0x7f {
			return "", badRequest(fmt.Sprintf("invalid %s: expected a plain string", label))
		}
	}
	return text, nil
}

// asSettingValue accepts the shapes a scalar field can hold: null, a boolean, a usable number, a
// string, or a list of strings.
func asSettingValue(value any, label string) (any, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case bool:
		return v, nil
	case float64:
		if math.IsInf(v, 0) || math.IsNaN(v) || math.Abs(v) > 1e15 {
			return nil, badRequest(fmt.Sprintf("invalid %s: not a usable number", label))
		}
		return v, nil
	case string:
		return asSettingString(v, label)
	case []any:
		if len(v) > 200 {
			return nil, badRequest(fmt.Sprintf("invalid %s: too many items", label))
		}
		out := make([]string, len(v))
		for i, item := range v {
			text, err := asSettingString(item, fmt.Sprintf("%s[%d]", label, i))
			if err != nil {
				return nil, err
			}
			out[i] = text
		}
		return out, nil
	default:
		return nil, badRequest(fmt.Sprintf("invalid %s: expected a primitive or a list of strings", label))
	}
}

// asArrayItem validates one new list entry and puts its fields in the spec's order.
func asArrayItem(value any, spec arrayItemSpec, label string) (object, error) {
	raw, err := asRecord(value)
	if err != nil {
		return nil, err
	}
	// Refuse unknown fields before shaping, so a typo ("descriptoin") is an error the user can
	// see rather than a silently dropped value. Sorted, because two unknown fields must always
	// produce the same message — an error that names a different key on every run is one nobody
	// can write a test for.
	for _, key := range sortedKeys(raw) {
		known := false
		for _, allowed := range spec.order {
			if key == allowed {
				known = true
				break
			}
		}
		if !known {
			return nil, badRequest(fmt.Sprintf("unknown field in %s: %s", label, key))
		}
	}

	var item object
	for _, key := range spec.order {
		value, present := raw[key]
		if !present || value == nil || value == "" {
			continue
		}
		if spec.lists[key] {
			list, ok := value.([]any)
			if !ok || len(list) > 200 {
				return nil, badRequest(fmt.Sprintf("invalid %s.%s: expected a list of strings", label, key))
			}
			texts := make([]string, len(list))
			for i, entry := range list {
				text, err := asSettingString(entry, fmt.Sprintf("%s.%s[%d]", label, key, i))
				if err != nil {
					return nil, err
				}
				texts[i] = text
			}
			item = append(item, field{key: key, value: texts})
			continue
		}
		text, err := asSettingString(value, fmt.Sprintf("%s.%s", label, key))
		if err != nil {
			return nil, err
		}
		item = append(item, field{key: key, value: text})
	}
	for _, key := range spec.required {
		if !item.has(key) {
			return nil, badRequest(fmt.Sprintf("%s needs a %s", label, key))
		}
	}
	return item, nil
}

func (o object) has(key string) bool {
	for _, f := range o {
		if f.key == key {
			return true
		}
	}
	return false
}

// asMap is the same entry as a plain map, for the copy the parser judges.
func (o object) asMap() map[string]any {
	out := make(map[string]any, len(o))
	for _, f := range o {
		out[f.key] = f.value
	}
	return out
}

// asIndex accepts a non-negative whole number. A float from JSON that is not whole is a page bug,
// not a position.
func asIndex(value any, label string) (int, error) {
	n, ok := value.(float64)
	if !ok || n != math.Trunc(n) || n < 0 || n > 1e6 {
		return 0, badRequest(label + ": index must be a non-negative integer")
	}
	return int(n), nil
}

// asExpect validates the safety-net fields for one list.
func asExpect(value any, path, label string) (map[string]string, error) {
	if value == nil {
		return map[string]string{}, nil
	}
	raw, err := asRecord(value)
	if err != nil {
		return nil, err
	}
	allowed := arrayRemoveKeys[path]
	expect := make(map[string]string, len(raw))
	for _, key := range sortedKeys(raw) {
		item := raw[key]
		ok := false
		for _, name := range allowed {
			if key == name {
				ok = true
				break
			}
		}
		if !ok {
			return nil, badRequest(fmt.Sprintf("%s: cannot match on '%s'", label, key))
		}
		text, err := asSettingString(item, fmt.Sprintf("%s.expect.%s", label, key))
		if err != nil {
			return nil, err
		}
		expect[key] = text
	}
	return expect, nil
}

// asOperations validates the browser's whole request.
func asOperations(value any) ([]settingsOp, error) {
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return nil, badRequest(`expected a non-empty "operations" array`)
	}
	if len(list) > maxOperations {
		return nil, badRequest("too many operations in one request")
	}
	ops := make([]settingsOp, 0, len(list))
	for i, raw := range list {
		record, err := asRecord(raw)
		if err != nil {
			return nil, err
		}
		label := fmt.Sprintf("operations[%d]", i)
		pathText, _ := record["path"].(string)
		op, _ := record["op"].(string)

		switch op {
		case "set":
			if !setPaths[pathText] {
				return nil, badRequest(fmt.Sprintf("%s: '%s' is not a field the wizard can set", label, pathText))
			}
			value, err := asSettingValue(record["value"], label+".value")
			if err != nil {
				return nil, err
			}
			ops = append(ops, settingsOp{op: "set", path: strings.Split(pathText, "."), value: value})

		case "unset":
			if !unsetPaths[pathText] {
				return nil, badRequest(fmt.Sprintf("%s: '%s' is not a field the wizard can clear", label, pathText))
			}
			ops = append(ops, settingsOp{op: "unset", path: strings.Split(pathText, ".")})

		case "add":
			spec, ok := arrayAddSpecs[pathText]
			if !ok {
				return nil, badRequest(fmt.Sprintf("%s: '%s' is not a list the wizard can add to", label, pathText))
			}
			item, err := asArrayItem(record["item"], spec, label+".item")
			if err != nil {
				return nil, err
			}
			ops = append(ops, settingsOp{op: "add", path: strings.Split(pathText, "."), item: item})

		case "setAt":
			// Edit in place: the same position-not-content selection as removeAt, and the same
			// `expect` safety net. The editable set is narrower than the add spec because an
			// entry's identity must not change under the things that point at it.
			fields, ok := arraySetFields[pathText]
			if !ok {
				return nil, badRequest(fmt.Sprintf("%s: '%s' is not a list the wizard can edit", label, pathText))
			}
			key, _ := record["key"].(string)
			editable := false
			for _, name := range fields {
				if key == name {
					editable = true
					break
				}
			}
			if !editable {
				return nil, badRequest(fmt.Sprintf("%s: '%s' is not editable on %s", label, key, pathText))
			}
			index, err := asIndex(record["index"], label)
			if err != nil {
				return nil, err
			}
			expect, err := asExpect(record["expect"], pathText, label)
			if err != nil {
				return nil, err
			}
			text, err := asSettingString(record["value"], label+".value")
			if err != nil {
				return nil, err
			}
			ops = append(ops, settingsOp{
				op: "setAt", path: strings.Split(pathText, "."),
				index: index, key: key, value: text, expect: expect,
			})

		case "removeAt":
			// Removal is by POSITION, not by content match: two entries can be
			// indistinguishable by fields when one's are a subset of the other's
			// (`[{ "repo": "x" }, { "repo": "x", "slug": "y" }]`), and a content match would
			// delete both. The page knows which row it rendered, so it sends that row's index;
			// `expect` is checked against whatever actually sits there.
			if _, ok := arrayRemoveKeys[pathText]; !ok {
				return nil, badRequest(fmt.Sprintf("%s: '%s' is not a list the wizard can remove from", label, pathText))
			}
			index, err := asIndex(record["index"], label)
			if err != nil {
				return nil, err
			}
			expect, err := asExpect(record["expect"], pathText, label)
			if err != nil {
				return nil, err
			}
			ops = append(ops, settingsOp{op: "removeAt", path: strings.Split(pathText, "."), index: index, expect: expect})

		default:
			return nil, badRequest(fmt.Sprintf("%s: unknown op %q", label, op))
		}
	}
	return ops, nil
}

/* ------------------------------------------------------------------ applying */

func deepGet(target map[string]any, path []string) any {
	var value any = target
	for _, key := range path {
		obj, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = obj[key]
	}
	return value
}

func deepSet(target map[string]any, path []string, value any) {
	obj := target
	for _, key := range path[:len(path)-1] {
		next, ok := obj[key].(map[string]any)
		if !ok {
			next = map[string]any{}
			obj[key] = next
		}
		obj = next
	}
	obj[path[len(path)-1]] = value
}

// applyOpsToInput is the candidate the parser judges: the file's own decoded input with the
// operations applied as plain map edits.
//
// It works on a copy, so the caller's input still describes the file on disk — that copy is what
// the post-write check compares the re-read file against. nil in (a file that did not decode)
// means nil out, and the textual edit then proceeds on the strength of the allow-list and the
// post-write verify alone.
func applyOpsToInput(input map[string]any, ops []settingsOp) map[string]any {
	if input == nil {
		return nil
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil
	}
	var target map[string]any
	if json.Unmarshal(encoded, &target) != nil {
		return nil
	}

	for _, op := range ops {
		switch op.op {
		case "set":
			deepSet(target, op.path, op.value)
		case "unset":
			// null, not a deleted key: it is what the textual edit writes, and the loader reads
			// the two the same way.
			deepSet(target, op.path, nil)
		case "add":
			if list, ok := deepGet(target, op.path).([]any); ok {
				deepSet(target, op.path, append(list, op.item.asMap()))
			} else {
				deepSet(target, op.path, []any{op.item.asMap()})
			}
		case "setAt":
			list, ok := deepGet(target, op.path).([]any)
			if !ok || op.index >= len(list) {
				continue
			}
			if entry, ok := list[op.index].(map[string]any); ok {
				entry[op.key] = op.value
			}
		case "removeAt":
			// Out of range leaves the list alone; the text edit then refuses too, and the
			// post-write comparison catches any divergence between the two.
			list, ok := deepGet(target, op.path).([]any)
			if !ok || op.index >= len(list) {
				continue
			}
			deepSet(target, op.path, append(append([]any{}, list[:op.index]...), list[op.index+1:]...))
		}
	}
	return target
}

// applyOpsToSource applies the same operations to the file's text, through the comment-preserving
// splices in edit.go. Nothing outside the fields being written moves.
func applyOpsToSource(source string, ops []settingsOp) (string, bool, error) {
	text := source
	changed := false
	for _, op := range ops {
		var result editResult
		var ok bool
		switch op.op {
		case "set":
			result, ok = setObjectField(text, op.path, renderValue(op.value))
		case "unset":
			result, ok = setObjectField(text, op.path, "null")
		case "add":
			result, ok = insertIntoArray(text, op.path, renderValue(op.item))
		case "setAt":
			result, ok = setArrayItemField(text, op.path, op.index, op.key, renderValue(op.value), op.expect)
		case "removeAt":
			var removed removeResult
			removed, ok = removeArrayItemAt(text, op.path, op.index, op.expect)
			result = removed.editResult
		}
		if !ok {
			return "", false, conflict(fmt.Sprintf(
				"could not apply %s at %s — the file's structure was not recognised; edit it by hand",
				op.op, op.pathText()))
		}
		text = result.text
		changed = changed || result.changed
	}
	return text, changed, nil
}
