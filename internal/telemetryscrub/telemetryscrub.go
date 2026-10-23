// Package telemetryscrub redacts log records before any handler sees them.
package telemetryscrub

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"
)

const Redacted = "[REDACTED]"

const (
	maxDepth    = 8
	maxElements = 64
	maxKeyCache = 4096
)

// Fragments are compared after lowercasing and removing every non-alphanumeric rune.
var protectedFragments = []string{
	"token", "secret", "password", "passwd", "credential", "authorization", "bearer",
	"cookie", "apikey", "signature", "jwt", "session", "privatekey",
}

var benignKeys = map[string]bool{
	"maxoutputtokens":                    true,
	"outputtokens":                       true,
	"inputtokens":                        true,
	"packettokens":                       true,
	"devhealthfactsblockerauthorization": true,
}

var networkSuffixes = []string{"remoteip", "clientip", "clientaddress", "remoteaddr", "remoteaddress"}

var valueShapes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]*`),
	regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]+-----.*?(?:-----END [A-Z0-9 ]+-----|\z)`),
	regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s/:@]+:[^\s/@]+@`),
	regexp.MustCompile(`\b(?:sk|ghp|gho|ghs|xox[abp])[-_][A-Za-z0-9_-]{16,}`),
}

// shapeMarkers gate the regexes: a value without any marker cannot match a shape.
var shapeMarkers = []string{"earer", "EARER", "eyJ", "-----", "://", "sk-", "sk_", "ghp_", "gho_", "ghs_", "xox"}

// ErrorClass names an error's dynamic type; the message is never returned.
func ErrorClass(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%T", err)
}

func compact(key string) string {
	var b strings.Builder
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 'a' - 'A')
		}
	}
	return b.String()
}

func hasDSNSegment(key string) bool {
	for _, seg := range strings.FieldsFunc(strings.ToLower(key), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if seg == "dsn" {
			return true
		}
	}
	return strings.HasSuffix(strings.ToLower(key), "dsn")
}

type keyClass uint8

const (
	classClean keyClass = iota
	classProtected
	classNetwork
)

func classify(key string) keyClass {
	c := compact(key)
	if !benignKeys[c] {
		for _, f := range protectedFragments {
			if strings.Contains(c, f) {
				return classProtected
			}
		}
		if hasDSNSegment(key) {
			return classProtected
		}
	}
	for _, s := range networkSuffixes {
		if strings.HasSuffix(c, s) {
			return classNetwork
		}
	}
	return classClean
}

var keyCache sync.Map
var keyCacheSize int64
var keyCacheMu sync.Mutex

func classifyCached(key string) keyClass {
	if v, ok := keyCache.Load(key); ok {
		return v.(keyClass)
	}
	c := classify(key)
	keyCacheMu.Lock()
	if keyCacheSize < maxKeyCache {
		keyCacheSize++
		keyCache.Store(key, c)
	}
	keyCacheMu.Unlock()
	return c
}

// classPath applies the key rules to a leaf key and to every group-qualified prefix.
func classPath(groups []string, key string) keyClass {
	out := classifyCached(key)
	if out == classProtected || len(groups) == 0 {
		return out
	}
	for i := range groups {
		if classify(groups[i]) == classProtected {
			return classProtected
		}
	}
	if out == classClean {
		if classify(strings.Join(append(append([]string{}, groups...), key), ".")) == classNetwork {
			return classNetwork
		}
	}
	return out
}

func mayContainShape(s string) bool {
	for _, m := range shapeMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

func scrubString(s string) string {
	if !mayContainShape(s) {
		return s
	}
	for _, re := range valueShapes {
		s = re.ReplaceAllString(s, Redacted)
	}
	return s
}

func networkPrefix(raw string) string {
	raw = strings.TrimSpace(raw)
	if p, err := netip.ParsePrefix(raw); err == nil {
		return p.Masked().String()
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return Redacted
	}
	addr = addr.WithZone("").Unmap()
	bits := 24
	if addr.Is6() {
		bits = 48
	}
	p, err := addr.Prefix(bits)
	if err != nil {
		return Redacted
	}
	return p.String()
}

var timeType = reflect.TypeOf(time.Time{})

// tree converts an opaque value to a scrubbed, structured copy; no format verb is applied to caller data.
func tree(v reflect.Value, depth int) any {
	if !v.IsValid() {
		return nil
	}
	if depth > maxDepth {
		return "[truncated]"
	}
	if v.CanInterface() {
		if err, ok := v.Interface().(error); ok {
			if v.Kind() == reflect.Pointer && v.IsNil() {
				return nil
			}
			return ErrorClass(err)
		}
		if t, ok := v.Interface().(time.Time); ok {
			return t
		}
		if d, ok := v.Interface().(time.Duration); ok {
			return d
		}
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return tree(v.Elem(), depth+1)
	case reflect.Bool:
		return v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint()
	case reflect.Float32, reflect.Float64:
		return v.Float()
	case reflect.String:
		return scrubString(v.String())
	case reflect.Map:
		out := map[string]any{}
		iter := v.MapRange()
		n := 0
		for iter.Next() {
			if n++; n > maxElements {
				out["[truncated]"] = true
				break
			}
			k := fmt.Sprint(tree(iter.Key(), depth+1))
			if classify(k) == classProtected {
				out[k] = Redacted
				continue
			}
			out[k] = tree(iter.Value(), depth+1)
		}
		return out
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
			return "[bytes]"
		}
		n := v.Len()
		if n > maxElements {
			n = maxElements
		}
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, tree(v.Index(i), depth+1))
		}
		return out
	case reflect.Struct:
		if v.Type() == timeType {
			return v.Interface()
		}
		out := map[string]any{}
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			if classify(f.Name) == classProtected {
				out[f.Name] = Redacted
				continue
			}
			out[f.Name] = tree(v.Field(i), depth+1)
		}
		return out
	default:
		return fmt.Sprintf("%T", v.Interface())
	}
}

func clean(groups []string, a slog.Attr) bool {
	switch a.Value.Kind() {
	case slog.KindString:
		return classPath(groups, a.Key) == classClean && !mayContainShape(a.Value.String())
	case slog.KindInt64, slog.KindUint64, slog.KindFloat64, slog.KindBool, slog.KindDuration, slog.KindTime:
		return classPath(groups, a.Key) == classClean
	}
	return false
}

func scrubAttr(groups []string, a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		if a.Key != "" && classify(a.Key) == classProtected {
			a.Value = slog.StringValue(Redacted)
			return a
		}
		inner := groups
		if a.Key != "" {
			inner = append(append([]string{}, groups...), a.Key)
		}
		group := a.Value.Group()
		out := make([]slog.Attr, 0, len(group))
		for _, g := range group {
			out = append(out, scrubAttr(inner, g))
		}
		a.Value = slog.GroupValue(out...)
		return a
	}
	switch classPath(groups, a.Key) {
	case classProtected:
		a.Value = slog.StringValue(Redacted)
		return a
	case classNetwork:
		a.Value = slog.StringValue(networkPrefix(a.Value.String()))
		return a
	}
	switch a.Value.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(scrubString(a.Value.String()))
	case slog.KindAny:
		a.Value = slog.AnyValue(tree(reflect.ValueOf(a.Value.Any()), 0))
	}
	return a
}

type handler struct {
	next   slog.Handler
	groups []string
}

// Handler returns next behind a scrubber: every attribute, record or bound, is scrubbed first.
func Handler(next slog.Handler) slog.Handler { return handler{next: next} }

func (h handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h handler) Handle(ctx context.Context, r slog.Record) error {
	msgClean := !mayContainShape(r.Message)
	allClean := msgClean
	if allClean {
		r.Attrs(func(a slog.Attr) bool {
			if !clean(h.groups, a) {
				allClean = false
			}
			return allClean
		})
	}
	if allClean {
		return h.next.Handle(ctx, r)
	}
	out := slog.NewRecord(r.Time, r.Level, scrubString(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(scrubAttr(h.groups, a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		out = append(out, scrubAttr(h.groups, a))
	}
	return handler{next: h.next.WithAttrs(out), groups: h.groups}
}

func (h handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return handler{next: h.next.WithGroup(name), groups: append(append([]string{}, h.groups...), name)}
}

// KeyClass reports how Handler treats a key, so a static scan shares the one rule set.
func KeyClass(key string) (protected, network bool) {
	switch classify(key) {
	case classProtected:
		return true, false
	case classNetwork:
		return false, true
	}
	return false, false
}
