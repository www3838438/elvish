package eval

import (
	"fmt"

	"github.com/elves/elvish/eval/types"
)

// Callable wraps the Call method.
type Callable interface {
	// Call calls the receiver in a Frame with arguments and options.
	Call(ec *Frame, args []types.Value, opts map[string]types.Value) error
}

var (
	// NoArgs is an empty argument list. It can be used as an argument to Call.
	NoArgs = []types.Value{}
	// NoOpts is an empty option map. It can be used as an argument to Call.
	NoOpts = map[string]types.Value{}
)

// Fn is a callable value.
type Fn interface {
	types.Value
	Callable
}

// FromJSONInterface converts a interface{} that results from json.Unmarshal to
// a Value.
func FromJSONInterface(v interface{}) types.Value {
	if v == nil {
		// TODO Use a more appropriate type
		return ""
	}
	switch v.(type) {
	case bool:
		return types.Bool(v.(bool))
	case float64, string:
		// TODO Use a numeric type for float64
		return fmt.Sprint(v)
	case []interface{}:
		a := v.([]interface{})
		vs := make([]types.Value, len(a))
		for i, v := range a {
			vs[i] = FromJSONInterface(v)
		}
		return types.MakeList(vs...)
	case map[string]interface{}:
		m := v.(map[string]interface{})
		mv := types.EmptyMapInner
		for k, v := range m {
			mv = mv.Assoc(k, FromJSONInterface(v))
		}
		return types.NewMap(mv)
	default:
		throw(fmt.Errorf("unexpected json type: %T", v))
		return nil // not reached
	}
}
