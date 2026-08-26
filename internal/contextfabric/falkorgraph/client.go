package falkorgraph

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	falkordb "github.com/FalkorDB/falkordb-go/v2"
)

// sdkAPI is the real conn: a compact-protocol result decoder over the
// pinned falkordb-go client (see the falkorgraph package doc comment for
// exactly why it never calls the client's high-level Graph.Query/
// CallProcedure -- no context support, ToString panics, broken
// CallProcedure, GraphSchema data race, all independently verified). Every
// call here goes through db.Conn.Do(ctx, ...) directly, so a real ctx
// (cancellation, deadline, the configured RequestTimeout) reaches the
// server on every request.
type sdkAPI struct {
	db     *falkordb.FalkorDB
	config Config
	// everConnected is set true the first time ANY command against this
	// connection succeeds. classifyConnError's CHAOS-3809 TLS-handshake
	// decoration is gated on this still being false -- see that method's
	// own doc comment for why (Codex round-1 P1: a blanket decoration
	// misdiagnosed an ordinary slow query on an already-working connection
	// as a TLS/plaintext mismatch).
	everConnected atomic.Bool
}

func newSDKAPI(config Config) (conn, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	options := &falkordb.ConnectionOption{
		Addr:         config.Addr,
		Password:     config.Password,
		PoolSize:     config.PoolSize,
		DialTimeout:  config.RequestTimeout,
		ReadTimeout:  config.RequestTimeout,
		WriteTimeout: config.RequestTimeout,
	}
	if config.TLS {
		options.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	db, err := falkordb.FalkorDBNew(options)
	if err != nil {
		return nil, safeDependencyError("connect to falkordb", err)
	}
	return &sdkAPI{db: db, config: config}, nil
}

// query is sdkAPI's only entry point for running Cypher. --compact is
// mandatory (the client's parser panics on the verbose reply shape,
// verified); the param header is built from safeParams' output, which is
// guaranteed to only ever contain the nine types falkordb-go's ToString
// supports without panicking.
func (s *sdkAPI) query(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
	safe, err := safeParams(params)
	if err != nil {
		return nil, fmt.Errorf("falkordb query parameters: %w", err)
	}
	text := cypher
	if len(safe) > 0 {
		text = falkordb.BuildParamsHeader(safe) + cypher
	}
	command := "GRAPH.QUERY"
	if readOnly {
		command = "GRAPH.RO_QUERY"
	}
	raw, err := s.db.Conn.Do(ctx, command, graphKey, text, "--compact").Result()
	if err != nil {
		return nil, s.classifyConnError("query context graph", err)
	}
	s.everConnected.Store(true)
	g := s.db.SelectGraph(graphKey)
	result, err := falkordb.QueryResultNew(g, raw)
	if err != nil {
		return nil, safeDependencyError("decode falkordb result", err)
	}
	rows := make([]row, 0)
	for result.Next() {
		record := result.Record()
		if record == nil {
			continue
		}
		decoded := make(row, len(record.Keys()))
		for _, key := range record.Keys() {
			value, _ := record.Get(key)
			decoded[key] = decodeValue(value)
		}
		rows = append(rows, decoded)
	}
	return rows, nil
}

func (s *sdkAPI) deleteGraph(ctx context.Context, graphKey string) error {
	err := s.db.Conn.Do(ctx, "GRAPH.DELETE", graphKey).Err()
	if err != nil {
		return s.classifyConnError("delete organization graph", err)
	}
	s.everConnected.Store(true)
	return nil
}

func (s *sdkAPI) listGraphs(ctx context.Context) ([]string, error) {
	names, err := s.db.Conn.Do(ctx, "GRAPH.LIST").StringSlice()
	if err != nil {
		return nil, s.classifyConnError("list graphs", err)
	}
	s.everConnected.Store(true)
	return names, nil
}

func (s *sdkAPI) constraints(ctx context.Context, graphKey string) ([]constraintStatus, error) {
	rows, err := s.query(ctx, graphKey, "CALL db.constraints()", nil, true)
	if err != nil {
		return nil, err
	}
	statuses := make([]constraintStatus, 0, len(rows))
	for _, r := range rows {
		statuses = append(statuses, constraintStatus{
			Type: rowString(r, "type"), Label: rowString(r, "label"),
			Properties: rowStringSlice(r, "properties"), EntityType: rowString(r, "entitytype"),
			Status: rowString(r, "status"),
		})
	}
	return statuses, nil
}

func (s *sdkAPI) indexes(ctx context.Context, graphKey string) ([]indexStatus, error) {
	rows, err := s.query(ctx, graphKey, "CALL db.indexes()", nil, true)
	if err != nil {
		return nil, err
	}
	statuses := make([]indexStatus, 0, len(rows))
	for _, r := range rows {
		types := make(map[string][]string)
		if raw, ok := r.get("types").(map[string]interface{}); ok {
			for property, value := range raw {
				if list, ok := value.([]interface{}); ok {
					kinds := make([]string, 0, len(list))
					for _, item := range list {
						if s, ok := item.(string); ok {
							kinds = append(kinds, s)
						}
					}
					types[property] = kinds
				}
			}
		}
		options, _ := r.get("options").(map[string]interface{})
		statuses = append(statuses, indexStatus{
			Label: rowString(r, "label"), Properties: rowStringSlice(r, "properties"),
			Types: types, EntityType: rowString(r, "entitytype"), Options: options,
			Status: rowString(r, "status"),
		})
	}
	return statuses, nil
}

func (s *sdkAPI) createIndex(ctx context.Context, graphKey, label string, properties []string, relationship bool) error {
	var cypher string
	if relationship {
		columns := make([]string, len(properties))
		for i, property := range properties {
			columns[i] = "r." + property
		}
		cypher = fmt.Sprintf("CREATE INDEX FOR ()-[r:%s]-() ON (%s)", label, strings.Join(columns, ", "))
	} else {
		columns := make([]string, len(properties))
		for i, property := range properties {
			columns[i] = "n." + property
		}
		cypher = fmt.Sprintf("CREATE INDEX FOR (n:%s) ON (%s)", label, strings.Join(columns, ", "))
	}
	_, err := s.query(ctx, graphKey, cypher, nil, false)
	if errors.Is(err, errAlreadyExists) {
		return nil
	}
	return err
}

func (s *sdkAPI) createConstraint(ctx context.Context, graphKey string, unique bool, entityType, label string, properties []string) error {
	kind := "MANDATORY"
	if unique {
		kind = "UNIQUE"
	}
	args := make([]interface{}, 0, 6+len(properties))
	args = append(args, "GRAPH.CONSTRAINT", "CREATE", graphKey, kind, entityType, label, "PROPERTIES", len(properties))
	for _, property := range properties {
		args = append(args, property)
	}
	err := s.db.Conn.Do(ctx, args...).Err()
	if err == nil {
		s.everConnected.Store(true)
		return nil
	}
	if errors.Is(s.classifyConnError("create constraint", err), errAlreadyExists) {
		return nil
	}
	return s.classifyConnError("create constraint", err)
}

func rowString(r row, key string) string {
	value, _ := r.get(key).(string)
	return value
}

func rowStringSlice(r row, key string) []string {
	raw, ok := r.get(key).([]interface{})
	if !ok {
		if direct, ok := r.get(key).([]string); ok {
			return direct
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// decodeValue converts one falkordb-go decoded scalar/composite value into
// this package's neutral shapes -- *falkordb.Node/*falkordb.Edge become
// this package's node/edge, everything else passes through as-is (falkordb-go
// already decodes compact-protocol scalars into plain Go string/int64/
// float64/bool/nil, and arrays/maps/paths into []interface{}/
// map[string]interface{}/falkordb.Path).
func decodeValue(value interface{}) interface{} {
	switch v := value.(type) {
	case *falkordb.Node:
		if v == nil {
			return nil
		}
		return &node{ID: v.ID, Labels: append([]string(nil), v.Labels...), Properties: normalizeProperties(v.Properties)}
	case *falkordb.Edge:
		if v == nil {
			return nil
		}
		return &edge{ID: v.ID, Relation: v.Relation, SourceID: v.SourceNodeID(), DestID: v.DestNodeID(), Properties: normalizeProperties(v.Properties)}
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, item := range v {
			out[i] = decodeValue(item)
		}
		return out
	default:
		return value
	}
}

// normalizeProperties converts every all-string []interface{} property value
// into []string. falkordb-go's own node/edge property parser always decodes
// a compact-protocol array as []interface{}, even when every element is a
// string and the writer's original Go value was a genuine []string --
// verified live: an "evidence_refs": []string{"x"} write round-trips back
// as []interface{}{"x"}. Left unconverted, every graphrank helper that type-
// asserts a property to []string (EvidenceRefs, the authorization_* wildcard
// convention in authorize.go) silently fails its assertion and treats a
// real, non-empty list as absent -- evidence closure and authorization both
// fail closed on data that was written correctly and only misread. Applied
// once here, at the lowest decode layer, so every caller of toCandidateNode/
// toCandidateEdge sees the same convention graphrank expects without having
// to re-derive this fix at each call site.
func normalizeProperties(properties map[string]interface{}) map[string]interface{} {
	if properties == nil {
		return nil
	}
	out := make(map[string]interface{}, len(properties))
	for key, value := range properties {
		out[key] = normalizePropertyValue(value)
	}
	return out
}

func normalizePropertyValue(value interface{}) interface{} {
	list, ok := value.([]interface{})
	if !ok {
		return value
	}
	strs := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			// Not an all-string list (or empty semantics differ) -- leave
			// it as []interface{} rather than guess; nothing in this
			// adapter currently writes a mixed-type or non-string list
			// property.
			return value
		}
		strs = append(strs, s)
	}
	return strs
}

// safeParams validates (and shallow-copies) params so that every value
// reaching falkordb-go's BuildParamsHeader/ToString is one of the nine types
// it supports without panicking: nil, string, int, int64, float64, bool,
// []interface{}, []string, map[string]interface{}. Verified live that
// ToString panics on int32, uint64, float32, []int64, map[string]string,
// and time.Time -- this package's own writers must never construct a param
// value outside this set (timestamps are always pre-formatted strings or
// int64 epoch-nanos, never time.Time; see identity.go).
func safeParams(params map[string]interface{}) (map[string]interface{}, error) {
	if params == nil {
		return nil, nil
	}
	out := make(map[string]interface{}, len(params))
	for key, value := range params {
		safe, err := safeParamValue(value)
		if err != nil {
			return nil, fmt.Errorf("parameter %q: %w", key, err)
		}
		out[key] = safe
	}
	return out, nil
}

func safeParamValue(value interface{}) (interface{}, error) {
	switch v := value.(type) {
	case nil, string, int, int64, float64, bool, []string:
		return v, nil
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, item := range v {
			safe, err := safeParamValue(item)
			if err != nil {
				return nil, err
			}
			out[i] = safe
		}
		return out, nil
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for key, item := range v {
			safe, err := safeParamValue(item)
			if err != nil {
				return nil, err
			}
			out[key] = safe
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported falkordb parameter type %T", value)
	}
}

// classifyConnError wraps classifyFalkorError with CHAOS-3809's TLS/
// plaintext-mismatch context: when this connection is configured for TLS and
// the classified result is a deadline-exceeded (safeDependencyError's
// deliberately bare-error path for context.Canceled/context.DeadlineExceeded
// below), a TLS handshake against a plaintext FalkorDB server hangs exactly
// the same way a slow or unreachable server would -- the discriminating
// evidence in the ticket showed both cases surface as an identical bare
// "context deadline exceeded", indistinguishable from each other. Naming the
// TLS setting here turns a 30-second guessing exercise (the incident this
// ticket documents) into a one-line diagnosis. errors.Is(result,
// context.DeadlineExceeded) still holds after this wrap -- it only adds
// explanation via %w, never replaces the sentinel -- so every existing
// caller checking for a deadline is unaffected.
//
// This decoration is gated on everConnected -- see everConnectedProofSentinels
// and isProofOfLife's own doc comments for the ALLOWLIST design that gate
// uses (team-lead-ordered after four Codex rounds each found a different
// way an earlier BLOCKLIST design let something that was NOT proof of a
// working connection slip through). Once ANY command has ever succeeded, or
// hit a proof-of-life classification, a later deadline is a query-level
// timeout on a connection already proven to work, never a handshake problem
// -- never decorated again.
//
// Known residual gap (Codex round-2, not closed, out of this ticket's
// scope): falkordb-go's FalkorDBNew runs its own internal isSentinel()
// probe (an INFO command) during construction, invisibly to sdkAPI -- if
// that hidden probe happens to succeed against a genuinely TLS-speaking
// server, everConnected has no way to observe it, so this connection's
// FIRST real command could still be misdiagnosed if it happens to be slow.
// Narrow (one possible misdiagnosis on cold start, self-correcting after
// the first real proof-of-life event) compared to the ticket's reported
// shape (every call, forever).
func (s *sdkAPI) classifyConnError(operation string, err error) error {
	classified := classifyFalkorError(operation, err)
	if isProofOfLife(err, classified) {
		s.everConnected.Store(true)
		return classified
	}
	if !s.config.TLS || s.everConnected.Load() || !errors.Is(classified, context.DeadlineExceeded) {
		return classified
	}
	return fmt.Errorf("%s: TLS handshake timed out -- %s is enabled for this connection; if the FalkorDB server does not speak TLS, set %s=false: %w",
		operation, EnvTLS, EnvTLS, classified)
}

// everConnectedProofSentinels is the CLOSED allowlist of classifications
// that can only be reached via a genuine FalkorDB protocol round-trip --
// each requires the server to have actually parsed and responded to a
// command, which is impossible unless the connection (and any TLS
// handshake) already succeeded.
//
// This is deliberately an allowlist, not a blocklist ("anything that isn't
// X or Y"). Four straight Codex rounds each found a different way a
// blocklist admitted a shape that was NOT real proof of a working
// connection: FalkorDB's own "Query timed out" response (round 1, fixed by
// treating it as its own exception rather than folding it into "not a
// deadline"), an idempotent already-exists reply reachable via a classified
// error rather than a nil one (round 2), context.Canceled -- which carries
// zero information about connection health, since a caller cancellation can
// land before, during, or after a real handshake succeeds (round 3) -- and,
// the design-breaking one, classifyFalkorError's generic/unclassified
// fallback (round 4): a genuine connection-refused, a dropped-mid-handshake
// EOF, or a TLS alert all reduce to that same generic fallback, and each is
// the STRONGEST possible signal the connection never worked -- the exact
// opposite of proof of life.
//
// A blocklist fails toward "assumed connected": any future or currently-
// unrecognized error shape defaults to proof of life, which is the unsafe
// direction -- a false positive here silently suppresses this ticket's
// whole diagnosis for a connection's entire remaining lifetime. An
// allowlist fails the other way: an unrecognized shape defaults to "not
// proof", so the worst case is an occasional, still-plausible TLS
// suggestion on a connection that actually works -- mild compared to the
// alternative. A classification ADDED to classifyFalkorError in the future
// without a deliberate decision here simply falls through to "not proof",
// never silently joins the allowlist.
//
// TestClassifyConnErrorEverConnectedProofOfLifeTable is this table's own
// regression test, covering every classification classifyFalkorError
// currently produces plus generic/unrecognized shapes -- extend BOTH
// together when classifyFalkorError gains a new case.
var everConnectedProofSentinels = []error{
	errAlreadyExists,
	ErrNotFound,
	ErrConstraintViolation,
	ErrUnauthorized,
	errIndexNotFound,
}

// isProofOfLife reports whether classified (the result of classifying raw
// through classifyFalkorError) is provable evidence that FalkorDB actually
// parsed and responded to a command -- see everConnectedProofSentinels' own
// doc comment for why this is a closed allowlist. "Query timed out" is
// checked against the ORIGINAL raw text rather than the classified
// sentinel, because classifyFalkorError maps it to the same
// context.DeadlineExceeded value a genuine pre-handshake dial timeout also
// produces -- errors.Is alone cannot tell the two apart, only the raw
// message can.
func isProofOfLife(raw error, classified error) bool {
	if raw != nil && strings.Contains(raw.Error(), "Query timed out") {
		return true
	}
	for _, sentinel := range everConnectedProofSentinels {
		if errors.Is(classified, sentinel) {
			return true
		}
	}
	return false
}

// classifyFalkorError maps FalkorDB's untyped, string-only error responses
// (verified: there is no typed error hierarchy for GRAPH.* commands) into
// this package's bounded sentinel errors, matching zepgraph's
// safeDependencyError/zepStatusCode posture -- dependency response bodies
// are never included in the returned error text.
func classifyFalkorError(operation string, err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "Query timed out"):
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	case strings.Contains(message, "unique constraint violation"):
		return fmt.Errorf("%s: %w", operation, ErrConstraintViolation)
	case strings.Contains(message, "already indexed") || strings.Contains(message, "already exists"):
		return fmt.Errorf("%s: %w", operation, errAlreadyExists)
	case strings.Contains(message, "no such index"):
		return fmt.Errorf("%s: %w", operation, errIndexNotFound)
	// "Invalid graph operation on empty key" is FalkorDB's error for a
	// GRAPH.DELETE or GRAPH.RO_QUERY against a graph key that never
	// existed or was just deleted. Verified live: unlike GRAPH.QUERY
	// (which silently auto-creates the key -- the §1.3(3) hazard),
	// GRAPH.RO_QUERY does NOT auto-create and instead returns this exact
	// error, giving every read-only lookup (ProjectionWatermark, GetNode
	// equivalents) an honest "not found" signal after a purge, with no
	// separate existence check required.
	case strings.Contains(message, "Invalid graph operation on empty key"):
		return fmt.Errorf("%s: %w", operation, ErrNotFound)
	case strings.Contains(message, "WRONGPASS") || strings.Contains(message, "NOAUTH"):
		return fmt.Errorf("%s: %w", operation, ErrUnauthorized)
	default:
		return safeDependencyError(operation, err)
	}
}

// safeDependencyError classifies a dependency failure without leaking its
// raw text -- mirrors zepgraph.safeDependencyError.
// knownSentinels is every classification classifyFalkorError (or a caller)
// may already have attached to err. safeDependencyError is commonly called
// a second time on an error a.api.query/deleteGraph already ran through
// classifyFalkorError -- callers check errors.Is(err, ErrNotFound) and
// similar, so re-flattening an already-classified error here would silently
// break every one of those checks (this exact bug shipped once: purge
// followed by ProjectionWatermark returned a generic dependency error
// instead of ErrNotFound because this function flattened it). Each known
// sentinel is preserved (re-wrapped with operation context, never bare) so
// %w chains still resolve; only a genuinely unclassified error gets the
// fully generic, content-free message.
var knownSentinels = []error{
	ErrNotFound, ErrUnauthorized, ErrRateLimited, ErrConstraintViolation,
	errAlreadyExists, errIndexNotFound, errConstraintBootstrapFailed, errConstraintBootstrapTimedOut,
	context.Canceled, context.DeadlineExceeded,
}

func safeDependencyError(operation string, err error) error {
	if err == nil {
		return nil
	}
	for _, sentinel := range knownSentinels {
		if errors.Is(err, sentinel) {
			if sentinel == context.Canceled || sentinel == context.DeadlineExceeded {
				return err
			}
			return fmt.Errorf("%s: %w", operation, sentinel)
		}
	}
	return fmt.Errorf("context fabric graph dependency error during %s", operation)
}
