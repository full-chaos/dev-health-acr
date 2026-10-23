// Package telemetryscrub redacts log records before any handler sees them.
package telemetryscrub

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
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
	"token", "secret", "password", "passwd", "passphrase", "credential", "authorization", "bearer",
	"cookie", "apikey", "signature", "jwt", "session", "privatekey", "signingkey", "encryptionkey",
	"awsaccesskeyid", "passcode", "verificationcode", "authcode", "otpcode", "mfacode", "accesskey", "authkey",
}

// protectedSegments match only as a whole word of the key (split on separators and camel case),
// so author and passenger stay clean.
var protectedSegments = map[string]bool{"dsn": true, "pwd": true, "otp": true, "pin": true, "mfa": true, "passcode": true}

var passSuffixDenied = map[string]bool{"bypass": true, "compass": true, "overpass": true, "underpass": true, "trespass": true, "encompass": true, "surpass": true, "impass": true}

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
	regexp.MustCompile(`(?i)\bbasic\s+[A-Za-z0-9+/=]{8,}`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]*`),
	regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]+-----.*?(?:-----END [A-Z0-9 ]+-----|\z)`),
	regexp.MustCompile(`\b(?:sk|ghp|gho|ghs|xox[abp])[-_][A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
}

// shapeMarkers are lowercase; a value is checked against them case-insensitively before any regex runs.
var shapeMarkers = []string{"earer", "asic", "eyj", "-----", "://", "sk-", "sk_", "ghp_", "gho_", "ghs_", "xox", "akia", "asia"}

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

func segments(key string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(key)
	for i, r := range rs {
		alnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if !alnum {
			flush()
			continue
		}
		upper := r >= 'A' && r <= 'Z'
		if upper && len(cur) > 0 {
			prev := rs[i-1]
			prevLower := prev >= 'a' && prev <= 'z' || prev >= '0' && prev <= '9'
			nextLower := i+1 < len(rs) && rs[i+1] >= 'a' && rs[i+1] <= 'z'
			if prevLower || (prev >= 'A' && prev <= 'Z' && nextLower) {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

func protectedBySegment(key, c string) bool {
	segs := segments(key)
	for _, seg := range segs {
		if protectedSegments[seg] {
			return true
		}
	}
	if len(segs) > 0 {
		if last := segs[len(segs)-1]; last == "auth" || last == "pass" {
			return true
		}
	}
	if strings.HasSuffix(c, "dsn") || strings.HasSuffix(c, "pwd") {
		return true
	}
	if strings.HasSuffix(c, "pass") && !passSuffixDenied[c] {
		return true
	}
	return false
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
		cf := strings.ReplaceAll(strings.ReplaceAll(c, "tokens", ""), "secretar", "")
		for _, f := range protectedFragments {
			if strings.Contains(cf, f) {
				return classProtected
			}
		}
		if protectedBySegment(key, c) {
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

// classPath applies the key rules to a leaf key and to every enclosing group: a protected
// or network ancestor taints every child.
func classPath(groups []string, key string) keyClass {
	out := classifyCached(key)
	if out == classProtected || len(groups) == 0 {
		return out
	}
	network := out == classNetwork
	for i := range groups {
		switch classify(groups[i]) {
		case classProtected:
			return classProtected
		case classNetwork:
			network = true
		}
	}
	switch classify(strings.Join(append(append([]string{}, groups...), key), ".")) {
	case classProtected:
		return classProtected
	case classNetwork:
		network = true
	}
	if network {
		return classNetwork
	}
	return classClean
}

func containsFold(s, lowerSub string) bool {
	n := len(lowerSub)
	for i := 0; i+n <= len(s); i++ {
		j := 0
		for ; j < n; j++ {
			c := s[i+j]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != lowerSub[j] {
				break
			}
		}
		if j == n {
			return true
		}
	}
	return false
}

func mayContainShape(s string) bool {
	for _, m := range shapeMarkers {
		if containsFold(s, m) {
			return true
		}
	}
	return false
}

// redactUserinfo replaces everything between "://" and the last "@" of the same whitespace-delimited token.
// looksLikeUserinfo tells credentials from a host-and-path prefix: "host/users/" and "host:8080/users/" are paths.
func looksLikeUserinfo(before string) bool {
	slash := strings.Index(before, "/")
	if slash < 0 {
		return true
	}
	colon := strings.Index(before, ":")
	if colon < 0 || colon > slash {
		return false
	}
	for _, r := range before[colon+1 : slash] {
		if r < '0' || r > '9' {
			return true
		}
	}
	return false
}

func redactUserinfo(s string) string {
	var out strings.Builder
	for {
		i := strings.Index(s, "://")
		if i < 0 {
			break
		}
		out.WriteString(s[:i+3])
		rest := s[i+3:]
		end := strings.IndexAny(rest, " \t\r\n")
		if end < 0 {
			end = len(rest)
		}
		token := rest[:end]
		if at := strings.LastIndex(token, "@"); at >= 0 && looksLikeUserinfo(token[:at]) {
			out.WriteString(Redacted + "@")
			token = token[at+1:]
		}
		out.WriteString(token)
		s = rest[end:]
	}
	out.WriteString(s)
	return out.String()
}

var kvPair = regexp.MustCompile(`(["']?([A-Za-z0-9_.-]+)["']?\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&"'}\]]+)`)

// kvWords: a key=value or "key":"value" pair can only name a protected key if one of these fragments appears.
var kvWords = []string{"token", "secret", "pass", "pwd", "key", "auth", "code", "otp", "pin", "mfa", "session", "credential", "cookie", "signature", "jwt", "dsn", "bearer"}

func mayContainPair(s string) bool {
	if !strings.ContainsAny(s, "=:") {
		return false
	}
	for _, w := range kvWords {
		if containsFold(s, w) {
			return true
		}
	}
	return false
}

var queryPair = regexp.MustCompile(`([?&])([A-Za-z0-9_.%+-]+)=([^\s&#"']*)`)

func redactQuery(s string) string {
	return queryPair.ReplaceAllStringFunc(s, func(m string) string {
		sub := queryPair.FindStringSubmatch(m)
		key := sub[2]
		if decoded, err := url.QueryUnescape(key); err == nil {
			key = decoded
		}
		if classify(key) == classProtected {
			return sub[1] + sub[2] + "=" + Redacted
		}
		return m
	})
}

func redactPairs(s string) string {
	s = redactQuery(s)
	return kvPair.ReplaceAllStringFunc(s, func(m string) string {
		sub := kvPair.FindStringSubmatch(m)
		if classify(sub[2]) == classProtected {
			return sub[1] + Redacted
		}
		return m
	})
}

func scrubString(s string) string {
	shape, pair := mayContainShape(s), mayContainPair(s)
	if !shape && !pair {
		return s
	}
	if shape {
		s = redactUserinfo(s)
		for _, re := range valueShapes {
			s = re.ReplaceAllString(s, Redacted)
		}
	}
	if pair {
		s = redactPairs(s)
	}
	return s
}

func networkPrefix(raw string) string {
	raw = strings.TrimSpace(raw)
	var addr netip.Addr
	if p, err := netip.ParsePrefix(raw); err == nil {
		addr = p.Addr()
	} else if a, err := netip.ParseAddr(raw); err == nil {
		addr = a
	} else {
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

// maxNodes bounds the work spent on the opaque values of one record.
const maxNodes = 512

type budget struct{ left int }

func (b *budget) take() bool {
	if b.left <= 0 {
		return false
	}
	b.left--
	return true
}

func scalarString(v reflect.Value) (string, bool) {
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return "", false
		}
		v = v.Elem()
	}
	if v.IsValid() && v.Kind() == reflect.String {
		return v.String(), true
	}
	return "", false
}

func keyedValue(path []string, k string, v reflect.Value, depth int, b *budget) any {
	switch classPath(path, k) {
	case classProtected:
		return Redacted
	case classNetwork:
		if str, ok := scalarString(v); ok {
			return networkPrefix(str)
		}
		return Redacted
	}
	return tree(v, depth+1, b, append(append([]string{}, path...), k))
}

func fieldNames(f reflect.StructField) []string {
	names := []string{f.Name}
	for _, tag := range []string{"json", "yaml", "mapstructure", "db"} {
		if name, _, _ := strings.Cut(f.Tag.Get(tag), ","); name != "" && name != "-" {
			names = append(names, name)
		}
	}
	return names
}

// tree converts an opaque value to a scrubbed, structured copy; no format verb is applied to caller data.
func tree(v reflect.Value, depth int, b *budget, path []string) any {
	if !v.IsValid() {
		return nil
	}
	if depth > maxDepth || !b.take() {
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
		return tree(v.Elem(), depth+1, b, path)
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
		for iter.Next() {
			if b.left <= 0 {
				out["[truncated]"] = true
				break
			}
			k := fmt.Sprint(tree(iter.Key(), depth+1, b, nil))
			out[k] = keyedValue(path, k, iter.Value(), depth, b)
		}
		return out
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return "[bytes]"
		}
		n := v.Len()
		if n > maxElements {
			n = maxElements
		}
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, tree(v.Index(i), depth+1, b, path))
		}
		return out
	case reflect.Struct:
		out := map[string]any{}
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			if b.left <= 0 {
				out["[truncated]"] = true
				break
			}
			class := classClean
			for _, name := range fieldNames(f) {
				switch c := classPath(path, name); {
				case c == classProtected:
					class = classProtected
				case c == classNetwork && class == classClean:
					class = classNetwork
				}
			}
			switch class {
			case classProtected:
				out[f.Name] = Redacted
			case classNetwork:
				if str, ok := scalarString(v.Field(i)); ok {
					out[f.Name] = networkPrefix(str)
				} else {
					out[f.Name] = Redacted
				}
			default:
				out[f.Name] = tree(v.Field(i), depth+1, b, append(append([]string{}, path...), f.Name))
			}
		}
		return out
	default:
		return fmt.Sprintf("%T", v.Interface())
	}
}

func clean(groups []string, a slog.Attr) bool {
	switch a.Value.Kind() {
	case slog.KindString:
		return classPath(groups, a.Key) == classClean && !mayContainShape(a.Value.String()) && !mayContainPair(a.Value.String())
	case slog.KindInt64, slog.KindUint64, slog.KindFloat64, slog.KindBool, slog.KindDuration, slog.KindTime:
		return classPath(groups, a.Key) == classClean
	}
	return false
}

func scrubAttr(groups []string, a slog.Attr, b *budget) slog.Attr {
	if a.Value.Kind() == slog.KindLogValuer {
		if err, ok := a.Value.Any().(error); ok && classPath(groups, a.Key) == classClean {
			a.Value = slog.StringValue(ErrorClass(err))
			return a
		}
	}
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
			out = append(out, scrubAttr(inner, g, b))
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
		a.Value = slog.AnyValue(tree(reflect.ValueOf(a.Value.Any()), 0, b, append(append([]string{}, groups...), a.Key)))
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
	msgClean := !mayContainShape(r.Message) && !mayContainPair(r.Message)
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
	b := &budget{left: maxNodes}
	out := slog.NewRecord(r.Time, r.Level, scrubString(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(scrubAttr(h.groups, a, b))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	b := &budget{left: maxNodes}
	out := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		out = append(out, scrubAttr(h.groups, a, b))
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

// ScrubText applies the value-shape redaction Handler uses to a string.
func ScrubText(s string) string { return scrubString(s) }
