package watchable

import (
	"fmt"
	"reflect"

	"google.golang.org/protobuf/proto"
)

type (
	_DeepCopier[T any] interface {
		DeepCopy() T
	}
	// DeepCopier[T] describes a type 'T' that has a 'DeepCopy() T' method; it is useful for
	// asserting that your DeepCopy method will be accepted by this package's DeepEqual
	// function.
	DeepCopier[T _DeepCopier[T]] _DeepCopier[T]
)

type (
	_Comparable[T any] interface {
		Equal(T) bool
	}
	// Comparable[T] describes a type 'T' that has an 'Equal(T) bool' method; it is useful for
	// asserting that your Equal method will be accepted by this package's DeepEqual function.
	//
	// The name of this interface mimics the built-in 'comparable' identifier, deviating from the
	// usual Go naming convention for single-method interfaces ('Equaler').
	Comparable[T _Comparable[T]] _Comparable[T]
)

func hasNoPointers(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128,
		reflect.String:
		return true
	case reflect.Array:
		return hasNoPointers(typ.Elem())
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			if !hasNoPointers(typ.Field(i).Type) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// DeepCopy returns a deep copy of a value.
//
// In order of precedence:
//
// - If the type 'T' has a 'DeepCopy' method (implements the 'DeepCopier[T]' interface), then that
//   method is used.
//
// - If the type 'T' implements google.golang.org/protobuf/proto.Message, then
//   google.golang.org/protobuf/proto.Clone is used.
//
// - If the value is a primitive ('bool', any of the 'int' types, either of the 'float' types,
//   either of the 'complex' types, or 'string'), or an array (not slice) or struct that only
//   contains primitives, then it is naively copied by value.
//
// - Otherwise, DeepCopy panics.
func DeepCopy[T any](val T) T {
	switch tval := any(val).(type) {
	case _DeepCopier[T]:
		return tval.DeepCopy()
	case proto.Message:
		return proto.Clone(tval).(T)
	default:
		if hasNoPointers(reflect.TypeOf(val)) {
			return val
		}
		panic(fmt.Errorf("watchable.DeepCopy: type is not copiable: %T", val))
	}
}

// DeepEqual returns whether two values are deeply equal.
//
// In order of precedence:
//
// - If the type 'T' has an 'Equal' method (implements the 'Comparable[T]' interface), then that
//   method is used.
//
// - If the types of 'a' and 'b' both implement google.golang.org/protobuf/proto.Message, then
//   google.golang.org/protobuf/proto.Equal is used.  (This is slightly different than saying "if
//   'T' implements proto.Message" because it could be that 'T' is an interface type, 'a' and 'b'
//   have differing concrete types, and only one of them implements proto.Message).
//
// - Otherwise, reflect.DeepEqual is used.
func DeepEqual[T any](a, b T) bool {
	switch ta := any(a).(type) {
	case _Comparable[T]:
		return ta.Equal(b)
	case proto.Message:
		if tb, ok := any(b).(proto.Message); ok {
			return proto.Equal(ta, tb)
		}
		return reflect.DeepEqual(a, b)
	default:
		return reflect.DeepEqual(a, b)
	}
}
