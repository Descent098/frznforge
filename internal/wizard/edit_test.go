package wizard

// The splice engine, ported from tests/unit/config-edit.test.ts.
//
// The contract is one sentence, and it outlives the change of language and of file format:
// after an edit, EVERY BYTE outside the edited field is identical. Comments, blank lines, key
// order, indentation, odd spacing around a colon, the trailing comma. A frznforge config is
// hand-written documentation as much as it is settings, and a wizard that reformats the file on
// save is a wizard nobody dares press the button on.
//
// So the assertions here compare the whole file against the original with one change applied by
// hand, rather than parsing the result and inspecting a value. A round-trip through a parser
// would pass just as happily for an editor that re-serialised the file from scratch — which is
// precisely the failure this suite exists to catch.
//
// The fixtures are deliberately awkward: a comment on the same line as a value, a commented-out
// block that looks exactly like the real one, a `//` inside a URL string, an escaped quote,
// spaces around a colon. That is the input the walkers exist to survive.

import (
	"encoding/json"
	"strings"
	"testing"

	"frznforge/internal/config"
)

// editFixture is the awkward config every splice test edits.
const editFixture = `// the whole site, hand-written, full of comments worth keeping
{
  "site": {
    "title": "My Forge", // shown in the sidebar
    "url": "https://example.com"
  },
  "owner": { "name": "Kieran \"K\" Wood", "handle": "kieran" },
  "theme": { "palette": "hearth" },
  // "site": { "title": "trap in a comment" },
  "repos": [
    // local things live here
    { "type": "local", "path": "../useful" },
    { "type": "github", "owner": "a", "repo": "b" }
  ],
  "organizations": [
    { "slug": "cc", "name": "Canadian Coding", "repos": ["useful"] }
  ],
  "ingest": {
    "maxBlobBytes"  :  524288, // half a meg, and the spacing around the colon is deliberate
    "maxCommits": null
  }
}
`

/* ------------------------------------------------------------------ helpers */

// edited unwraps a splice's (result, ok) pair, failing the test when the editor declined.
//
// Curried because Go can only spread a two-value call into a function whose parameters are
// exactly those two values, so `t` has to arrive in a call of its own.
func edited(t *testing.T, what string) func(editResult, bool) editResult {
	return func(result editResult, ok bool) editResult {
		t.Helper()
		if !ok {
			t.Fatalf("%s: the editor refused a file it should have been able to edit", what)
		}
		return result
	}
}

// wantFile compares a whole file, reporting the first line that differs. Whole-file comparison
// is the point of this suite, so a failure has to make one stray byte findable.
func wantFile(t *testing.T, got, want, what string) {
	t.Helper()
	if got == want {
		return
	}
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
		g, w := "<past the end of the file>", "<past the end of the file>"
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if g != w {
			t.Fatalf("%s: bytes outside the edited field moved\n  line %d got:  %q\n  line %d want: %q", what, i+1, g, i+1, w)
		}
	}
	t.Fatalf("%s: the files differ but no line does", what)
}

// wantContains is the human-readable half of an assertion: it names the exact bytes a reader can
// go and look for in the fixture above.
func wantContains(t *testing.T, text, want, what string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Errorf("%s: %q is not in the result", what, want)
	}
}

// wantParses proves the edit left a file the loader can still read — belt and braces on top of
// the byte comparison, since a splice that balanced its brackets by luck would still fail here.
func wantParses(t *testing.T, text, what string) {
	t.Helper()
	if !json.Valid(config.StripJSONC(config.TrimBOM([]byte(text)))) {
		t.Errorf("%s: the edited file is no longer valid JSONC:\n%s", what, text)
	}
}

/* ------------------------------------------------------------------ rendering */

func TestRenderValue(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{42, "42"},
		{float64(524288), "524288"}, // a JSON number arrives as float64 and must not become 524288.0
		{true, "true"},
		{nil, "null"},
		{"it's", `"it's"`},
		{[]string{"a", "b"}, `["a", "b"]`},
		{object{{key: "hot", value: 3}, {key: "name", value: "o'brien"}}, `{ "hot": 3, "name": "o'brien" }`},
		{object{}, "{}"},
	}
	for _, c := range cases {
		if got := renderValue(c.value); got != c.want {
			t.Errorf("renderValue(%#v) = %s, want %s", c.value, got, c.want)
		}
	}
}

func TestJSONQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a\nb", `"a\nb"`},
		{`say "hi"`, `"say \"hi\""`},
		{`C:\tmp`, `"C:\\tmp"`},
		{"tab\there", `"tab\there"`},
		{"\x01", `"\u0001"`},
		// encoding/json would write \u003c\u003e\u0026 here. A config file is full of URLs and
		// prose and is meant to be hand-edited afterwards, so the escaping stops at what JSON
		// actually requires.
		{"a<b>&c", `"a<b>&c"`},
	}
	for _, c := range cases {
		if got := jsonQuote(c.in); got != c.want {
			t.Errorf("jsonQuote(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

// A shape the wizard never writes is a programming error, not a user-facing one: it must fail
// loudly here rather than emit something the config schema then has to refuse.
func TestRenderValuePanicsOnAnUnsupportedShape(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("renderValue accepted a map, which it has no way to render deterministically")
		}
	}()
	renderValue(map[string]string{"a": "b"})
}

/* ------------------------------------------------------------------ setObjectField */

func TestSetObjectFieldReplacesOnlyTheTargetedValue(t *testing.T) {
	out := edited(t, "set site.title")(setObjectField(editFixture, []string{"site", "title"}, jsonQuote("New Name")))
	if !out.changed {
		t.Error("changed = false for a value that really did change")
	}
	wantFile(t, out.text, strings.Replace(editFixture, `"My Forge"`, `"New Name"`, 1), "set site.title")
	// The three decoys in the fixture, spelled out because they are the reason the walkers exist.
	wantContains(t, out.text, `"title": "New Name", // shown in the sidebar`, "the trailing comment")
	wantContains(t, out.text, `// "site": { "title": "trap in a comment" },`, "the commented-out block")
	wantContains(t, out.text, `"url": "https://example.com"`, "the // inside a URL string")
}

func TestSetObjectFieldReplacesANullAndLeavesItsNeighbourAlone(t *testing.T) {
	out := edited(t, "set ingest.maxCommits")(setObjectField(editFixture, []string{"ingest", "maxCommits"}, "50"))
	wantFile(t, out.text, strings.Replace(editFixture, `"maxCommits": null`, `"maxCommits": 50`, 1), "set ingest.maxCommits")
	wantContains(t, out.text, `"maxBlobBytes"  :  524288, // half a meg`, "the odd spacing and its comment")
}

func TestSetObjectFieldKeepsTheSpacingAroundTheFieldItRewrites(t *testing.T) {
	// Only the VALUE's bytes move: the two spaces either side of the colon are outside the span,
	// so a save cannot quietly normalise the author's formatting.
	out := edited(t, "set ingest.maxBlobBytes")(setObjectField(editFixture, []string{"ingest", "maxBlobBytes"}, "1024"))
	wantFile(t, out.text, strings.Replace(editFixture, "524288", "1024", 1), "set ingest.maxBlobBytes")
	wantContains(t, out.text, `"maxBlobBytes"  :  1024, // half a meg`, "the spacing and the comment after a rewrite")
}

func TestSetObjectFieldReportsANoOpAndTouchesNothing(t *testing.T) {
	out := edited(t, "no-op set")(setObjectField(editFixture, []string{"theme", "palette"}, jsonQuote("hearth")))
	if out.changed {
		t.Error("changed = true for a field that already said what was asked for; a no-op must not take a backup or count as a write")
	}
	wantFile(t, out.text, editFixture, "no-op set")
}

func TestSetObjectFieldCreatesAMissingLeafInsideAnExistingBlock(t *testing.T) {
	heat := renderValue(object{{key: "hot", value: 3}, {key: "warm", value: 30}, {key: "neutral", value: 180}, {key: "cool", value: 365}})
	out := edited(t, "create theme.heat")(setObjectField(editFixture, []string{"theme", "heat"}, heat))
	if !out.changed {
		t.Error("changed = false for a leaf that had to be created")
	}
	want := strings.Replace(editFixture,
		`  "theme": { "palette": "hearth" },`,
		"  \"theme\": { \"palette\": \"hearth\",\n    \"heat\": { \"hot\": 3, \"warm\": 30, \"neutral\": 180, \"cool\": 365 },\n  },",
		1)
	wantFile(t, out.text, want, "create theme.heat")
	wantParses(t, out.text, "create theme.heat")
}

func TestSetObjectFieldCreatesAWholeMissingChainAtTheRoot(t *testing.T) {
	out := edited(t, "create markdown.mermaid")(setObjectField(editFixture, []string{"markdown", "mermaid"}, "false"))
	// The new block lands after the last real value, with a trailing comma — legal in this
	// dialect, and the reason an append never has to rewrite the byte before it.
	want := strings.Replace(editFixture, "  }\n}\n", "  },\n  \"markdown\": { \"mermaid\": false },\n}\n", 1)
	wantFile(t, out.text, want, "create markdown.mermaid")
	wantParses(t, out.text, "create markdown.mermaid")
	open, close, ok := findRootObject(out.text)
	if !ok || close <= open {
		t.Errorf("the file no longer has one balanced root object: open=%d close=%d ok=%v", open, close, ok)
	}
}

func TestSetObjectFieldRefusesAFileItCannotNavigate(t *testing.T) {
	cases := []struct{ what, source string }{
		{"an array at the root", `[1, 2]`},
		{"a bare string", `"just a string"`},
		{"nothing but a comment", "// no config here\n"},
		{"an unclosed root object", "{\n  \"site\": {\n"},
		{"a site that is not an object", `{ "site": "not an object" }`},
	}
	for _, c := range cases {
		if _, ok := setObjectField(c.source, []string{"site", "title"}, `"x"`); ok {
			t.Errorf("%s: the editor claimed it could splice a file it cannot navigate", c.what)
		}
	}
}

func TestSetObjectFieldIgnoresARootObjectInsideACommentOrString(t *testing.T) {
	source := `// old: { "site": { "title": "COMMENTED" } }
{
  "site": { "title": "Real" },
  "note": "{ \"site\": { \"title\": \"STRING\" } }"
}
`
	out := edited(t, "edit past a commented-out root")(setObjectField(source, []string{"site", "title"}, jsonQuote("Edited")))
	wantFile(t, out.text, strings.Replace(source, `"Real"`, `"Edited"`, 1), "edit past a commented-out root")
	wantContains(t, out.text, `"COMMENTED"`, "the commented-out block")
	wantContains(t, out.text, `\"STRING\"`, "the object hiding in a string value")
}

/* ------------------------------------------------------------------ removeArrayItemAt */

// hostingFixture is the subset trap: entry 0's fields are a subset of entry 1's, so a content
// match on { "repo": "docs" } would delete both.
const hostingFixture = `{
  "hosting": {
    "sites": [
      { "repo": "docs" },
      { "repo": "docs", "slug": "documentation", "branch": "docs" }
    ]
  }
}
`

func TestRemoveArrayItemAtRemovesExactlyTheIndexedElement(t *testing.T) {
	out, ok := removeArrayItemAt(hostingFixture, []string{"hosting", "sites"}, 0, map[string]string{"repo": "docs"})
	if !ok {
		t.Fatal("the editor refused a removal it should have made")
	}
	if out.removed != 1 {
		t.Errorf("removed = %d, want 1 — a content match would have taken both rows", out.removed)
	}
	// Exactly the element's own bytes plus its separating comma.
	want := strings.Replace(hostingFixture, "\n      { \"repo\": \"docs\" },", "", 1)
	wantFile(t, out.text, want, "remove hosting.sites[0]")
	wantContains(t, out.text, `{ "repo": "docs", "slug": "documentation", "branch": "docs" }`, "the row that was not pointed at")
}

func TestRemoveArrayItemAtRemovesTheLastElementWithThePrecedingComma(t *testing.T) {
	// The last element has no trailing comma of its own, so it takes the one BEFORE it —
	// otherwise the element above is left with a dangling comma against the closing bracket.
	out, ok := removeArrayItemAt(hostingFixture, []string{"hosting", "sites"}, 1,
		map[string]string{"repo": "docs", "slug": "documentation"})
	if !ok {
		t.Fatal("the editor refused a removal it should have made")
	}
	if out.removed != 1 {
		t.Errorf("removed = %d, want 1", out.removed)
	}
	want := strings.Replace(hostingFixture,
		",\n      { \"repo\": \"docs\", \"slug\": \"documentation\", \"branch\": \"docs\" }\n    ", "", 1)
	wantFile(t, out.text, want, "remove hosting.sites[1]")
	wantParses(t, out.text, "remove hosting.sites[1]")
}

func TestRemoveArrayItemAtRefusesAStaleSelection(t *testing.T) {
	// A page working from a list that has changed under it must never delete the wrong row.
	cases := []struct {
		what   string
		index  int
		expect map[string]string
	}{
		{"a field the element carries with a different value", 0, map[string]string{"repo": "other"}},
		{"an index past the end", 9, map[string]string{}},
		{"a negative index", -1, map[string]string{}},
	}
	for _, c := range cases {
		if _, ok := removeArrayItemAt(hostingFixture, []string{"hosting", "sites"}, c.index, c.expect); ok {
			t.Errorf("%s: the removal went ahead anyway", c.what)
		}
	}
}

func TestRemoveArrayItemAtKeepsCommentsAndNeighbours(t *testing.T) {
	// repos[1] is the github entry; repos[0]'s span carries the comment above it, so removing
	// its neighbour must leave that comment exactly where the author put it.
	out, ok := removeArrayItemAt(editFixture, []string{"repos"}, 1,
		map[string]string{"type": "github", "owner": "a", "repo": "b"})
	if !ok {
		t.Fatal("the editor refused a removal it should have made")
	}
	// The closing bracket moves up onto the surviving element's line: the last element's span
	// runs to the `]`, so the whitespace before it leaves with the element.
	want := strings.Replace(editFixture, ",\n    { \"type\": \"github\", \"owner\": \"a\", \"repo\": \"b\" }\n  ]", "]", 1)
	wantFile(t, out.text, want, "remove repos[1]")
	wantContains(t, out.text, "// local things live here", "the comment inside the array")
	wantContains(t, out.text, `"maxBlobBytes"  :  524288, // half a meg`, "a field in a different block")
	wantParses(t, out.text, "remove repos[1]")
}

func TestRemoveArrayItemAtEmptiesAnArrayCleanly(t *testing.T) {
	out, ok := removeArrayItemAt(editFixture, []string{"organizations"}, 0, map[string]string{"slug": "cc"})
	if !ok {
		t.Fatal("the editor refused a removal it should have made")
	}
	wantContains(t, out.text, `"organizations": [],`, "the emptied array")
	wantParses(t, out.text, "empty organizations")
}

func TestRemoveArrayItemAtRequiresEveryExpectedFieldToMatch(t *testing.T) {
	// One wrong field is enough to refuse: the safety net is an AND, not a best guess.
	if _, ok := removeArrayItemAt(editFixture, []string{"repos"}, 1,
		map[string]string{"type": "github", "owner": "a", "repo": "wrong"}); ok {
		t.Error("a removal went ahead with a field that does not match the element at that index")
	}
}

func TestRemoveArrayItemAtIsANoOpForAnAbsentPath(t *testing.T) {
	// "Nothing to remove" is not an error — the caller reports it as a no-op, and the file must
	// come back untouched rather than half-created.
	out, ok := removeArrayItemAt(editFixture, []string{"contributors"}, 0, map[string]string{})
	if !ok {
		t.Fatal("an absent list was reported as an unreadable structure")
	}
	if out.changed || out.removed != 0 {
		t.Errorf("changed=%v removed=%d for a list the file does not have", out.changed, out.removed)
	}
	wantFile(t, out.text, editFixture, "remove from an absent list")
}

/* ------------------------------------------------------------------ insertIntoArray */

func TestInsertIntoArrayAppendsAndLeavesPresentItemsIdentical(t *testing.T) {
	item := renderValue(object{{key: "slug", value: "new-org"}, {key: "name", value: "New Org"}})
	out := edited(t, "append to organizations")(insertIntoArray(editFixture, []string{"organizations"}, item))
	if !out.changed {
		t.Error("changed = false for an append that added an entry")
	}
	want := strings.Replace(editFixture,
		"    { \"slug\": \"cc\", \"name\": \"Canadian Coding\", \"repos\": [\"useful\"] }\n",
		"    { \"slug\": \"cc\", \"name\": \"Canadian Coding\", \"repos\": [\"useful\"] },\n"+
			"    { \"slug\": \"new-org\", \"name\": \"New Org\" },\n", 1)
	wantFile(t, out.text, want, "append to organizations")
	wantContains(t, out.text, "// local things live here", "the comment in the untouched repos array")
	wantParses(t, out.text, "append to organizations")
}

func TestInsertIntoArrayCreatesAMissingArrayAndItsParents(t *testing.T) {
	item := renderValue(object{{key: "repo", value: "my-site"}, {key: "branch", value: "gh-pages"}})
	out := edited(t, "create hosting.sites")(insertIntoArray(editFixture, []string{"hosting", "sites"}, item))
	want := strings.Replace(editFixture, "  }\n}\n",
		"  },\n  \"hosting\": { \"sites\": [{ \"repo\": \"my-site\", \"branch\": \"gh-pages\" }] },\n}\n", 1)
	wantFile(t, out.text, want, "create hosting.sites")
	wantParses(t, out.text, "create hosting.sites")
}

/* ------------------------------------------------------------------ setArrayItemField */

const orgFixture = `{
  "organizations": [
    // the first one matters
    { "slug": "acme", "name": "Acme", "repos": ["a"] },  // trailing note
    { "slug": "beta", "name": "Beta Co" }
  ],
  "ingest": { "maxBlobBytes"  :  524288 }
}
`

func TestSetArrayItemFieldReplacesOneFieldAndNothingElse(t *testing.T) {
	out := edited(t, "setAt organizations[0].name")(
		setArrayItemField(orgFixture, []string{"organizations"}, 0, "name", jsonQuote("Acme Renamed"), nil))
	if !out.changed {
		t.Error("changed = false for a field that really did change")
	}
	wantFile(t, out.text, strings.Replace(orgFixture, `"Acme"`, `"Acme Renamed"`, 1), "setAt organizations[0].name")
	// Spelled out: the comment above the entry, the same-line note after it, the sibling entry,
	// and a field in a different block.
	wantContains(t, out.text, `{ "slug": "acme", "name": "Acme Renamed", "repos": ["a"] },  // trailing note`, "the edited row")
	wantContains(t, out.text, "// the first one matters", "the comment above the entry")
	wantContains(t, out.text, `{ "slug": "beta", "name": "Beta Co" }`, "the sibling entry")
	wantContains(t, out.text, `"maxBlobBytes"  :  524288`, "a field in another block")
}

func TestSetArrayItemFieldAddsAFieldTheEntryDidNotHave(t *testing.T) {
	out := edited(t, "setAt organizations[1].avatar")(
		setArrayItemField(orgFixture, []string{"organizations"}, 1, "avatar", jsonQuote("images/beta.png"), nil))
	want := strings.Replace(orgFixture,
		`{ "slug": "beta", "name": "Beta Co" }`,
		`{ "slug": "beta", "name": "Beta Co", "avatar": "images/beta.png" }`, 1)
	wantFile(t, out.text, want, "setAt organizations[1].avatar")
	wantParses(t, out.text, "setAt organizations[1].avatar")
}

func TestSetArrayItemFieldIsANoOpWhenTheValueAlreadyMatches(t *testing.T) {
	out := edited(t, "no-op setAt")(setArrayItemField(orgFixture, []string{"organizations"}, 0, "name", jsonQuote("Acme"), nil))
	if out.changed {
		t.Error("changed = true for a value that was already there")
	}
	wantFile(t, out.text, orgFixture, "no-op setAt")
}

func TestSetArrayItemFieldRefusesAnIndexItCannotResolve(t *testing.T) {
	for _, index := range []int{2, -1} {
		if _, ok := setArrayItemField(orgFixture, []string{"organizations"}, index, "name", `"x"`, nil); ok {
			t.Errorf("index %d: the editor guessed instead of refusing", index)
		}
	}
	if _, ok := setArrayItemField(orgFixture, []string{"contributors"}, 0, "name", `"x"`, nil); ok {
		t.Error("a list the file does not have was edited anyway")
	}
}

func TestSetArrayItemFieldRefusesWhenExpectDoesNotDescribeTheEntry(t *testing.T) {
	// The index selects; expect catches a page working from a stale list.
	if _, ok := setArrayItemField(orgFixture, []string{"organizations"}, 0, "name", `"x"`,
		map[string]string{"slug": "beta"}); ok {
		t.Error("the edit landed on an entry that does not match the caller's expectation")
	}
	if _, ok := setArrayItemField(orgFixture, []string{"organizations"}, 0, "name", `"x"`,
		map[string]string{"slug": "acme"}); !ok {
		t.Error("a matching expect was refused")
	}
}

func TestSetArrayItemFieldRefusesAnElementThatIsNotAnObject(t *testing.T) {
	// A position in the source only means something when the element is an object literal.
	for _, source := range []string{
		"{\n  \"organizations\": [\n    \"cc\"\n  ]\n}\n",
		"{\n  \"organizations\": [\n    [1, 2]\n  ]\n}\n",
	} {
		if _, ok := setArrayItemField(source, []string{"organizations"}, 0, "name", `"x"`, nil); ok {
			t.Errorf("a non-object element was edited:\n%s", source)
		}
	}
}

func TestSetArrayItemFieldKeepsASiblingFieldsExactBytes(t *testing.T) {
	source := `{
  "hosting": { "sites": [
    { "repo": "a",   "slug": "a-site" }
  ] }
}
`
	out := edited(t, "setAt hosting.sites[0].branch")(
		setArrayItemField(source, []string{"hosting", "sites"}, 0, "branch", jsonQuote("gh-pages"), nil))
	wantFile(t, out.text, strings.Replace(source,
		`{ "repo": "a",   "slug": "a-site" }`,
		`{ "repo": "a",   "slug": "a-site", "branch": "gh-pages" }`, 1), "setAt hosting.sites[0].branch")
}

func TestSetArrayItemFieldEscapesWhatItWrites(t *testing.T) {
	// Whatever a name contains, the file has to stay parseable — and the value has to come back
	// out of the parser unchanged.
	name := "O'Brien \"&\" Co\\slash"
	out := edited(t, "setAt with a hostile name")(
		setArrayItemField(orgFixture, []string{"organizations"}, 0, "name", jsonQuote(name), nil))
	wantContains(t, out.text, `"name": `+jsonQuote(name), "the escaped value")
	wantParses(t, out.text, "setAt with a hostile name")

	var decoded struct {
		Organizations []struct{ Name string } `json:"organizations"`
	}
	if err := json.Unmarshal(config.StripJSONC([]byte(out.text)), &decoded); err != nil {
		t.Fatalf("the edited file no longer decodes: %v", err)
	}
	if got := decoded.Organizations[0].Name; got != name {
		t.Errorf("the name came back as %q, want %q — the escaping is lossy", got, name)
	}
}

/* ------------------------------------------------------------------ composition */

func TestSeveralEditsEachTouchOnlyTheirOwnField(t *testing.T) {
	text := editFixture
	text = edited(t, "compose 1")(setObjectField(text, []string{"site", "title"}, jsonQuote("Composed"))).text
	text = edited(t, "compose 2")(setObjectField(text, []string{"theme", "heat"}, renderValue(object{
		{key: "hot", value: 2}, {key: "warm", value: 30}, {key: "neutral", value: 180}, {key: "cool", value: 365},
	}))).text
	text = edited(t, "compose 3")(insertIntoArray(text, []string{"organizations"},
		renderValue(object{{key: "slug", value: "x"}, {key: "name", value: "X"}}))).text
	removed, ok := removeArrayItemAt(text, []string{"repos"}, 1, map[string]string{"type": "github", "owner": "a"})
	if !ok {
		t.Fatal("compose 4: the removal was refused")
	}
	text = removed.text

	wantContains(t, text, `"title": "Composed", // shown in the sidebar`, "the first edit")
	wantContains(t, text, `"hot": 2`, "the second edit")
	wantContains(t, text, `{ "slug": "x", "name": "X" },`, "the third edit")
	wantContains(t, text, `"maxBlobBytes"  :  524288, // half a meg`, "a field none of the four edits named")
	wantContains(t, text, "// local things live here", "a comment none of the four edits named")
	wantParses(t, text, "four composed edits")

	// And the file still holds exactly one balanced root object, ending at its last byte.
	open, close, found := findRootObject(text)
	if !found || close != len(strings.TrimRight(text, "\n"))-1 {
		t.Errorf("root object spans %d..%d of %d bytes — the brackets no longer balance", open, close, len(text))
	}
}
