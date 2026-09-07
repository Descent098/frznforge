package wizard

import _ "embed"

// pageHTML is the wizard's entire user interface: one self-contained document with inline CSS
// and inline vanilla JavaScript, no build step, no CDN, no framework.
//
// It is served verbatim and nothing is ever interpolated into it, so it cannot carry an
// injection: everything the page knows arrives over /api/*, and every provider-supplied string
// goes onto it through textContent. Embedding it in the binary is also what makes `frznforge
// init --web` work from a single file with nothing beside it on disk.
//
//go:embed page.html
var pageHTML string
