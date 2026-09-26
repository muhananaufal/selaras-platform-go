package authz

import (
	"encoding/json"
	"reflect"
)

// sameJSON says whether two authorization models mean the same.
//
// OpenFGA returns a stored model with every empty default filled in -
// "object":"", "condition":"", "module":"", null metadata, empty relation
// maps - that the model as written leaves out. Both are reduced to what they
// state before comparing: empty strings, nulls, empty lists and empty
// objects are dropped, except the empty object under "this", which is not
// empty in meaning - it is a direct relation.
func sameJSON(a, b []byte) (bool, error) {
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, &y); err != nil {
		return false, err
	}
	return reflect.DeepEqual(canonical(x), canonical(y)), nil
}

// canonical drops what states nothing; see sameJSON. It returns nil for a
// value that is itself empty.
func canonical(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, child := range t {
			if k == "this" {
				out[k] = map[string]any{}
				continue
			}
			if c := canonical(child); c != nil {
				out[k] = c
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, child := range t {
			if c := canonical(child); c != nil {
				out = append(out, c)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case string:
		if t == "" {
			return nil
		}
		return t
	case nil:
		return nil
	default:
		return t
	}
}
