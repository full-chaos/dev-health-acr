package v1

import (
	"fmt"
	"reflect"
	"time"
)

// Representable-instant enforcement for PROJECTION INGRESS (CHAOS-3781
// round-6 R6-1).
//
// Every temporal comparison downstream converts through UnixNano, which is
// undefined outside the epoch-nanosecond range. A value outside it does not
// saturate, it WRAPS -- a year-9999 episode interval lands on a plausible
// instant, silently corrupting historical admission and tombstone ordering.
//
// R5-3 added this bound to entities and tombstones by hand, and the hand
// list missed contents and episodes -- which is the same
// enumerate-by-inspection failure this branch has hit in four other places.
// So the enumeration is DERIVED rather than written: reflection walks a
// projection value and finds every time.Time it actually contains,
// including fields nobody has added yet.
//
// This is not new machinery in the sense the investment cap forbids; it
// replaces a list that was already wrong with the same list computed
// correctly.

// projectionInstantWalkDepth bounds the reflective walk. Projection types
// nest a few levels at most (batch -> slice -> struct -> optional pointer),
// so this is generous while making a pathological or cyclic shape
// terminate rather than hang a validator.
//
// Round-7 F1: reaching the cap is an ERROR, not a stopping point. The walk
// used to return nil there, which silently ACCEPTED every timestamp below
// the cap -- so a field nested one level too deep would have been admitted
// unchecked, and the validator would have reported success having examined
// less than it claimed. That is the same fails-toward-fine shape as a sweep
// whose pattern stopped matching: indistinguishable from a clean result.
//
// Failing loud makes the cap self-reporting. A future type that genuinely
// nests deeper breaks a test rather than quietly losing its bound, and
// whoever adds it decides deliberately between restructuring the type and
// raising this number -- with the walk still total either way.
const projectionInstantWalkDepth = 8

var timeType = reflect.TypeOf(time.Time{})

// validateRepresentableInstants reports the first timestamp anywhere in
// value that cannot survive conversion to epoch nanoseconds.
//
// A nil pointer is ABSENT and skipped. A PRESENT ZERO is refused (round-7
// F2), for the same reason the engine boundary refuses one: the zero time
// is year 1, which is outside the representable range, and a pointer
// already expresses absence.
//
// Evidence for the semantics, rather than assumption -- swept all three
// production producers (devhealthsource clickhouse.go, tables.go,
// episodes.go):
//
//   - No producer can emit a zero today. Every nullable timestamp goes
//     through validity.go's (isNotNull, ifNull) pair, whose optionalTime
//     returns NIL when absent and never inspects the value; no bare
//     time.Time scan target is fed by a Nullable or LEFT-joined column.
//   - Absence is already expressed exclusively as a nil pointer on
//     ValidFrom/ValidTo.
//   - The contract ALREADY draws this line: validateTimeRange skips nil
//     and errors on value.IsZero(), so a pointer-to-zero is explicitly
//     illegal while a nil pointer is explicitly legal.
//   - Episodes cannot carry a zero EndedAt for an unfinished episode --
//     the Postgres column is NOT NULL with a CHECK, and ACR records
//     episodes post-hoc, so no in-progress state exists to need a
//     sentinel.
//
// So this refuses nothing any producer legitimately emits, and it makes
// the walk agree with the rule the rest of the contract already applies.
func validateRepresentableInstants(value any) error {
	return walkInstants(reflect.ValueOf(value), "", 0)
}

func walkInstants(value reflect.Value, path string, depth int) error {
	if !value.IsValid() {
		return nil
	}
	if depth > projectionInstantWalkDepth {
		return fmt.Errorf("%s nests deeper than the walk examines (%d levels); its timestamps would be admitted unchecked, so the batch is refused rather than partially validated",
			instantPathLabel(path), projectionInstantWalkDepth)
	}
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface:
		if value.IsNil() {
			return nil
		}
		return walkInstants(value.Elem(), path, depth+1)
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := walkInstants(value.Index(index), fmt.Sprintf("%s[%d]", path, index), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		for _, key := range value.MapKeys() {
			if err := walkInstants(value.MapIndex(key), path+"."+key.String(), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Struct:
		if value.Type() == timeType {
			instant, ok := value.Interface().(time.Time)
			if !ok || representableInstant(instant) {
				return nil
			}
			if instant.IsZero() {
				return fmt.Errorf("%s is present but zero; absence is expressed as a nil pointer, and the zero time is year 1 -- outside the representable range", instantPathLabel(path))
			}
			return fmt.Errorf("%s is outside the representable range (%s..%s)",
				instantPathLabel(path), minRepresentableInstant.Format("2006-01-02"), maxRepresentableInstant.Format("2006-01-02"))
		}
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			if field.PkgPath != "" {
				continue // unexported: nothing a producer can set
			}
			if err := walkInstants(value.Field(index), path+"."+field.Name, depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return nil
	}
}

func instantPathLabel(path string) string {
	if path == "" {
		return "timestamp"
	}
	return "timestamp at " + path
}
