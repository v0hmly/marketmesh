package runtime

import "reflect"

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}

	kind := reflect.TypeOf(value).Kind()
	switch kind {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(value).IsNil()
	default:
		return false
	}
}
