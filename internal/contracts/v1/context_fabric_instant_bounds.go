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
const projectionInstantWalkDepth = 8

var timeType = reflect.TypeOf(time.Time{})

// validateRepresentableInstants reports the first timestamp anywhere in
// value that cannot survive conversion to epoch nanoseconds.
//
// A nil pointer is ABSENT and skipped. A non-nil ZERO timestamp is NOT
// skipped here -- callers that treat a present zero as meaningful validate
// it themselves; what this refuses is only the unrepresentable, so it
// composes with the existing zero checks instead of duplicating them.
func validateRepresentableInstants(value any) error {
	return walkInstants(reflect.ValueOf(value), "", 0)
}

func walkInstants(value reflect.Value, path string, depth int) error {
	if depth > projectionInstantWalkDepth || !value.IsValid() {
		return nil
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
			if !ok || instant.IsZero() || representableInstant(instant) {
				return nil
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
