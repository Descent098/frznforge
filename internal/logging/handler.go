package logging

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// The handler stack, from the outside in:
//
//	scrubHandler  — every record passes through redaction exactly once, before any sink
//	  fanout      — stderr and the file are independent sinks with independent levels
//	    TextHandler(stderr) at the --log level, present only when the user asked for it
//	    TextHandler(file)   at debug, present on every run
//
// Scrubbing sits ABOVE the fan-out rather than inside each sink so a record is redacted once
// instead of once per sink, and so adding a third sink later cannot add a third way to leak.

// scrubHandler redacts a record on its way to inner.
//
// sealed records that an enclosing WithGroup was named like a secret. `WithGroup("auth")`
// followed by `With("header", tok)` binds an attribute whose own key ("header") says nothing,
// and only the group it landed in makes it a credential — so the group has to be remembered
// rather than inspected one attribute at a time.
type scrubHandler struct {
	inner  slog.Handler
	sealed bool
}

func (h scrubHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h scrubHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, Scrub(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.clean(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

// WithAttrs scrubs at binding time. slog.With("token", t) attaches the value once and every
// later record inherits it, so a handler that only scrubbed in Handle would let that one
// through — the attrs bound here are never seen by Handle again.
func (h scrubHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = h.clean(a)
	}
	return scrubHandler{inner: h.inner.WithAttrs(clean), sealed: h.sealed}
}

func (h scrubHandler) WithGroup(name string) slog.Handler {
	return scrubHandler{inner: h.inner.WithGroup(name), sealed: h.sealed || SecretKey(name)}
}

func (h scrubHandler) clean(a slog.Attr) slog.Attr {
	if h.sealed {
		return slog.String(a.Key, Redacted)
	}
	return scrubAttr(a)
}

func scrubAttr(a slog.Attr) slog.Attr {
	// Resolve first: a slog.LogValuer hides its real value behind a method, and scrubbing the
	// wrapper instead of the value would be scrubbing nothing.
	v := a.Value.Resolve()
	if SecretKey(a.Key) && v.Kind() != slog.KindGroup {
		return slog.String(a.Key, Redacted)
	}
	switch v.Kind() {
	case slog.KindGroup:
		// A group's own key does not carry a value, but a group named "auth" holding a "header"
		// would slip past a per-attribute check, so the group key qualifies its children too.
		wholeGroupIsSecret := SecretKey(a.Key)
		src := v.Group()
		clean := make([]slog.Attr, len(src))
		for i, g := range src {
			if wholeGroupIsSecret {
				clean[i] = slog.String(g.Key, Redacted)
				continue
			}
			clean[i] = scrubAttr(g)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(clean...)}
	case slog.KindString:
		return slog.String(a.Key, Scrub(v.String()))
	case slog.KindAny:
		// Errors and Stringers are the shapes that usually carry a URL — and a URL is where a
		// token hides. Anything else of unknown type is formatted the way the handler would
		// format it, scrubbed, and only substituted if the scrub actually changed something:
		// that keeps the output byte-identical in the overwhelmingly common case where there was
		// no secret, without leaving a hole for a []string of remote URLs.
		switch x := v.Any().(type) {
		case error:
			return slog.String(a.Key, Scrub(x.Error()))
		case fmt.Stringer:
			return slog.String(a.Key, Scrub(x.String()))
		default:
			text := fmt.Sprintf("%+v", x)
			if clean := Scrub(text); clean != text {
				return slog.String(a.Key, clean)
			}
		}
	}
	// Everything else — ints, bools, durations, times — cannot contain a secret, and is passed
	// through untouched so the handler formats it as it normally would.
	return slog.Attr{Key: a.Key, Value: v}
}

// fanout writes one record to several handlers, each with its own level.
//
// slog.Handler has no multi-writer in the standard library, and an io.MultiWriter underneath a
// single handler is not the same thing: the two sinks need different levels (stderr at whatever
// the user asked for, the file always at debug) and different timestamp formats.
type fanout struct{ hs []slog.Handler }

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f.hs {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, h := range f.hs {
		// Each sink re-checks its own level: Enabled above answered for the union, so a record
		// that only cleared the file's debug bar must not also reach a stderr handler set to warn.
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		// Clone, because a handler may retain or mutate the record's attr slice.
		if err := h.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Handler, len(f.hs))
	for i, h := range f.hs {
		out[i] = h.WithAttrs(attrs)
	}
	return fanout{hs: out}
}

func (f fanout) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, len(f.hs))
	for i, h := range f.hs {
		out[i] = h.WithGroup(name)
	}
	return fanout{hs: out}
}

// sinks is the process's logging state.
//
// Setup and SetupFile are called at different moments and in an order this package does not
// control: the CLI installs the stderr sink from --log before it has read the config, and only
// learns the output directory afterwards. Holding both sinks here and rebuilding the default
// logger from whichever exist means neither call can undo the other.
var sinks struct {
	mu     sync.Mutex
	stderr slog.Handler
	file   slog.Handler
}

// setSink replaces one sink and reinstalls slog's default logger. Pass nil to remove a sink.
func setSink(which *slog.Handler, h slog.Handler) {
	sinks.mu.Lock()
	defer sinks.mu.Unlock()
	*which = h

	var live []slog.Handler
	for _, s := range []slog.Handler{sinks.stderr, sinks.file} {
		if s != nil {
			live = append(live, s)
		}
	}
	switch len(live) {
	case 0:
		slog.SetDefault(slog.New(discard{}))
	case 1:
		slog.SetDefault(slog.New(scrubHandler{inner: live[0]}))
	default:
		slog.SetDefault(slog.New(scrubHandler{inner: fanout{hs: live}}))
	}
}
