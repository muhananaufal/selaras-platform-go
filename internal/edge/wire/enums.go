package wire

import (
	"strconv"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/muhananaufal/selaras-platform-go/internal/edge/rpcerr"
)

// UndefinedEnums lists every enum field in msg whose value is not one the
// schema defines.
//
// The strict codec refuses unknown enum NAMES, but a number is accepted as it
// is - protojson takes {"sex": 7} and the binary encoding carries only
// numbers - so an undefined value can still arrive. Downstream it would be
// read as "none of the known cases", which in several places means a
// default. It is refused here instead, for every request, without a rule to
// remember per field.
//
// Field names in the result are the JSON names the client sent, with a dot
// path for nested messages and an index for lists.
func UndefinedEnums(msg protoreflect.Message) []rpcerr.FieldViolation {
	var out []rpcerr.FieldViolation
	walk(msg, "", &out)
	return out
}

func walk(msg protoreflect.Message, prefix string, out *[]rpcerr.FieldViolation) {
	// google.protobuf.Value and Struct hold free-form JSON, never enums.
	if msg.Descriptor().FullName() == (&structpb.Value{}).ProtoReflect().Descriptor().FullName() {
		return
	}

	msg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		name := join(prefix, fd.JSONName())

		switch {
		case fd.IsList():
			list := v.List()
			for i := range list.Len() {
				check(fd, list.Get(i), name+"["+strconv.Itoa(i)+"]", out)
			}
		case fd.IsMap():
			v.Map().Range(func(k protoreflect.MapKey, mv protoreflect.Value) bool {
				check(fd.MapValue(), mv, name+"["+k.String()+"]", out)
				return true
			})
		default:
			check(fd, v, name, out)
		}
		return true
	})
}

func check(fd protoreflect.FieldDescriptor, v protoreflect.Value, name string, out *[]rpcerr.FieldViolation) {
	switch fd.Kind() {
	case protoreflect.EnumKind:
		if fd.Enum().Values().ByNumber(v.Enum()) == nil {
			*out = append(*out, rpcerr.FieldViolation{
				Field:       name,
				Description: "This value is not one of the allowed values.",
			})
		}
	case protoreflect.MessageKind, protoreflect.GroupKind:
		walk(v.Message(), name, out)
	default:
	}
}

func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}
