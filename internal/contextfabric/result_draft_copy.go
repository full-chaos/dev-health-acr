package contextfabric

import "reflect"

// copyResultDeep returns a copy of result that shares no slice, map or pointer
// target with it. Nil and empty are kept apart (the result contract tells them
// apart), and a struct is copied as a value first so unexported members such as
// a time.Time's travel with it.
//
// It exists for the re-assembly the member cut runs: the synthesizer's draft
// is assembled once per pass, assembly appends to and rewrites its slices, and
// a draft assembled twice must not carry the first assembly's writes.
func copyResultDeep(result InvestigationResult) InvestigationResult {
	source := reflect.ValueOf(result)
	target := reflect.New(source.Type()).Elem()
	copyValueDeep(target, source)
	return target.Interface().(InvestigationResult)
}

func copyValueDeep(target, source reflect.Value) {
	switch source.Kind() {
	case reflect.Slice:
		if source.IsNil() {
			target.Set(reflect.Zero(source.Type()))
			return
		}
		copied := reflect.MakeSlice(source.Type(), source.Len(), source.Len())
		for index := 0; index < source.Len(); index++ {
			copyValueDeep(copied.Index(index), source.Index(index))
		}
		target.Set(copied)
	case reflect.Map:
		if source.IsNil() {
			target.Set(reflect.Zero(source.Type()))
			return
		}
		copied := reflect.MakeMapWithSize(source.Type(), source.Len())
		iterator := source.MapRange()
		for iterator.Next() {
			value := reflect.New(source.Type().Elem()).Elem()
			copyValueDeep(value, iterator.Value())
			copied.SetMapIndex(iterator.Key(), value)
		}
		target.Set(copied)
	case reflect.Pointer:
		if source.IsNil() {
			target.Set(reflect.Zero(source.Type()))
			return
		}
		copied := reflect.New(source.Type().Elem())
		copyValueDeep(copied.Elem(), source.Elem())
		target.Set(copied)
	case reflect.Struct:
		target.Set(source)
		for index := 0; index < source.NumField(); index++ {
			if source.Type().Field(index).IsExported() {
				copyValueDeep(target.Field(index), source.Field(index))
			}
		}
	case reflect.Interface:
		if source.IsNil() {
			target.Set(reflect.Zero(source.Type()))
			return
		}
		inner := reflect.New(source.Elem().Type()).Elem()
		copyValueDeep(inner, source.Elem())
		target.Set(inner)
	default:
		target.Set(source)
	}
}
