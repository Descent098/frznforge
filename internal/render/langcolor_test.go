package render

import (
	"html/template"
	"testing"
)

// TestLangColorNeverEmitsZgotmplZ — html/template's CSS filter rejects `var(...)`, so returning a
// plain string here rendered the literal text "ZgotmplZ" into a style attribute for every
// language the colour map does not know. Neither test fixture contains such a language, which is
// exactly why it survived review.
func TestLangColorNeverEmitsZgotmplZ(t *testing.T) {
	hex := "#00ADD8"
	weird := "javascript:alert(1)"
	empty := ""
	cases := []struct {
		name string
		in   SummaryLang
		want template.CSS
	}{
		{"known hex", SummaryLang{Color: &hex}, template.CSS("#00ADD8")},
		{"no colour", SummaryLang{Color: nil}, template.CSS("var(--hf-lang-other)")},
		{"empty colour", SummaryLang{Color: &empty}, template.CSS("var(--hf-lang-other)")},
		{"hostile value", SummaryLang{Color: &weird}, template.CSS("var(--hf-lang-other)")},
	}
	for _, c := range cases {
		if got := LangColor(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestCardRendersFallbackColour is the end-to-end half: the template must actually paint the
// fallback, not the filter's placeholder.
func TestCardRendersFallbackColour(t *testing.T) {
	tpl := template.Must(template.New("t").Funcs(template.FuncMap{"langColor": LangColor}).
		Parse(`<i style="background: {{langColor .}}"></i>`))
	var out testWriter
	if err := tpl.Execute(&out, SummaryLang{Name: "Ignore List", Color: nil}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != `<i style="background: var(--hf-lang-other)"></i>` {
		t.Errorf("got %s", got)
	}
}

type testWriter struct{ b []byte }

func (w *testWriter) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }
func (w *testWriter) String() string              { return string(w.b) }
