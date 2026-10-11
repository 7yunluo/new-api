package toolconv

import (
	"fmt"
	"reflect"
	"strings"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

type schemaPointerRewrite struct{ from, to string }

// OpenAI function schemas accept a single schema in items. Convert bounded,
// homogeneous legacy tuples without widening their accepted values. Work on a
// copy: channel retries must continue to see the original Claude schema.
func inputSchemaToOpenAIParameters(input any) (any, error) {
	if input == nil {
		return nil, nil
	}
	encoded, err := kitutil.Marshal(input)
	if err != nil {
		return nil, err
	}
	var schema map[string]any
	if err := kitutil.Unmarshal(encoded, &schema); err != nil {
		return nil, err
	}
	var rewrites []schemaPointerRewrite
	var references []map[string]any
	if err := normalizeOpenAITupleSchema(schema, "#", 0, &rewrites, &references); err != nil {
		return nil, err
	}
	for _, node := range references {
		ref := node["$ref"].(string)
		for _, rewrite := range rewrites {
			if ref == rewrite.from || strings.HasPrefix(ref, rewrite.from+"/") {
				ref = rewrite.to + strings.TrimPrefix(ref, rewrite.from)
			}
		}
		node["$ref"] = ref
	}
	return schema, nil
}

func normalizeOpenAITupleSchema(schema map[string]any, path string, depth int, rewrites *[]schemaPointerRewrite, references *[]map[string]any) error {
	if depth > 128 {
		return fmt.Errorf("tool input schema exceeds maximum nesting depth")
	}
	if ref, ok := schema["$ref"].(string); ok && strings.HasPrefix(ref, "#/") {
		*references = append(*references, schema)
	}
	// Only descend through schema-valued keywords. Defaults, examples and enums
	// are instance data, even when they contain keys named items or $ref.
	for _, key := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"} {
		children, _ := schema[key].(map[string]any)
		for name, child := range children {
			if node, ok := child.(map[string]any); ok {
				escaped := strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
				if err := normalizeOpenAITupleSchema(node, path+"/"+key+"/"+escaped, depth+1, rewrites, references); err != nil {
					return err
				}
			}
		}
	}
	for _, key := range []string{"items", "prefixItems", "allOf", "anyOf", "oneOf", "additionalItems", "additionalProperties", "contains", "not", "if", "then", "else", "propertyNames", "unevaluatedItems", "unevaluatedProperties"} {
		switch child := schema[key].(type) {
		case map[string]any:
			if err := normalizeOpenAITupleSchema(child, path+"/"+key, depth+1, rewrites, references); err != nil {
				return err
			}
		case []any:
			for i, item := range child {
				if node, ok := item.(map[string]any); ok {
					if err := normalizeOpenAITupleSchema(node, fmt.Sprintf("%s/%s/%d", path, key, i), depth+1, rewrites, references); err != nil {
						return err
					}
				}
			}
		}
	}
	items, ok := schema["items"].([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	// Without a bound, replacing a tuple would constrain additional elements.
	maxItems, bounded := schema["maxItems"].(float64)
	if !bounded || maxItems > float64(len(items)) {
		return nil
	}
	if _, ok := items[0].(map[string]any); !ok {
		return nil
	}
	for _, item := range items[1:] {
		if !reflect.DeepEqual(items[0], item) {
			return nil
		}
	}
	schema["items"] = items[0]
	delete(schema, "additionalItems")
	for i := range items {
		*rewrites = append(*rewrites, schemaPointerRewrite{fmt.Sprintf("%s/items/%d", path, i), path + "/items"})
	}
	return nil
}
