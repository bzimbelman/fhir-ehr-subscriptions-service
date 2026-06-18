// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package merger applies precedence between configuration layers field-by-field.
//
// Per docs/low-level-design/configuration.md S5: the boot path passes
// (defaults, file, env, cli) into Merge in that order. Maps merge deeply;
// arrays and scalars are replaced wholesale by the higher-precedence layer.
package merger

// Merge folds layers in argument order — earlier are lower precedence,
// later are higher. Higher layers override lower at the leaf, but maps merge
// deeply so a higher layer that touches one nested key does not erase its
// siblings.
//
// Arrays/slices are replaced by the higher layer (wholesale, not element-wise).
// A type mismatch (e.g., low has a map, high has a string) resolves to the
// higher layer's value.
func Merge(layers ...map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for _, layer := range layers {
		mergeInto(out, layer)
	}
	return out
}

func mergeInto(dst, src map[string]interface{}) {
	for k, v := range src {
		existing, has := dst[k]
		if !has {
			dst[k] = cloneValue(v)
			continue
		}
		dst[k] = mergeValue(existing, v)
	}
}

// mergeValue merges a higher-precedence value into the lower-precedence one.
// Two maps merge deeply; everything else is wholesale replacement by the
// higher layer.
func mergeValue(low, high interface{}) interface{} {
	lowMap, lowOk := low.(map[string]interface{})
	highMap, highOk := high.(map[string]interface{})
	if lowOk && highOk {
		dst := map[string]interface{}{}
		mergeInto(dst, lowMap)
		mergeInto(dst, highMap)
		return dst
	}
	return cloneValue(high)
}

// cloneValue returns a deep copy of v so the caller's tree cannot be mutated
// through the merged result. Cheap because configuration trees are small.
func cloneValue(v interface{}) interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, vv := range x {
			out[k] = cloneValue(vv)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, item := range x {
			out[i] = cloneValue(item)
		}
		return out
	default:
		return v
	}
}
