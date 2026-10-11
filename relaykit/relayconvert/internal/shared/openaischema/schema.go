package openaischema

import (
	"fmt"
	"strconv"
	"strings"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

type schemaPointerRewrite struct{ from, to string }

// Some OpenAI-compatible providers reject tuple-form items. Convert legacy
// tuple schemas to prefixItems without widening or narrowing their accepted
// values. Work on a copy: channel retries must continue to see the original
// Claude schema.
func InputSchemaToParameters(input any) (any, error) {
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
	flattenRootUnionSchema(schema)
	for _, node := range references {
		ref := node["$ref"].(string)
		for _, rewrite := range rewrites {
			if ref == rewrite.from || strings.HasPrefix(ref, rewrite.from+"/") {
				ref = rewrite.to + strings.TrimPrefix(ref, rewrite.from)
			}
		}
		node["$ref"] = ref
	}
	// Some OpenAI-compatible validators reject references that point through
	// schema-valued array items or across sibling union branches. Resolve
	// internal references once so every branch is self-contained.
	resolveSchemaReferences(schema, schema, make(map[string]any), 0)
	return schema, nil
}

func resolveSchemaReferences(root, schema map[string]any, resolving map[string]any, depth int) {
	if depth > 128 {
		return
	}
	if ref, ok := schema["$ref"].(string); ok && strings.HasPrefix(ref, "#/") {
		target, ok := resolveSchemaPointer(root, ref)
		if !ok {
			return
		}
		if _, active := resolving[ref]; active {
			return
		}
		resolving[ref] = nil
		resolveSchemaReferences(root, target, resolving, depth+1)
		delete(resolving, ref)
		replacement := make(map[string]any, len(target))
		for key, value := range target {
			replacement[key] = value
		}
		delete(replacement, "$ref")
		clear(schema)
		for key, value := range replacement {
			schema[key] = value
		}
		return
	}
	resolving[schemaPointerID(schema)] = nil
	defer delete(resolving, schemaPointerID(schema))
	for _, key := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"} {
		children, _ := schema[key].(map[string]any)
		for _, child := range children {
			if node, ok := child.(map[string]any); ok {
				resolveSchemaReferences(root, node, resolving, depth+1)
			}
		}
	}
	for _, key := range []string{"items", "prefixItems", "allOf", "anyOf", "oneOf", "additionalItems", "additionalProperties", "contains", "not", "if", "then", "else", "propertyNames", "unevaluatedItems", "unevaluatedProperties"} {
		switch child := schema[key].(type) {
		case map[string]any:
			resolveSchemaReferences(root, child, resolving, depth+1)
		case []any:
			for _, item := range child {
				if node, ok := item.(map[string]any); ok {
					resolveSchemaReferences(root, node, resolving, depth+1)
				}
			}
		}
	}
}

func schemaPointerID(schema map[string]any) string {
	return fmt.Sprintf("%p", schema)
}

func resolveSchemaPointer(root map[string]any, ref string) (map[string]any, bool) {
	parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
	var current any = root
	for _, part := range parts {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			child, exists := node[part]
			if !exists {
				return nil, false
			}
			current = child
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(node) {
				return nil, false
			}
			current = node[index]
		default:
			return nil, false
		}
	}
	target, ok := current.(map[string]any)
	return target, ok
}

func flattenRootUnionSchema(schema map[string]any) {
	// Several OpenAI-compatible providers require an object at the root of
	// function parameters. A root union is valid JSON Schema, but wrapping it
	// in a single-property object preserves the choices while satisfying that
	// validator.
	union, hasUnion := schema["anyOf"].([]any)
	if !hasUnion {
		union, hasUnion = schema["oneOf"].([]any)
	}
	if !hasUnion {
		union, hasUnion = schema["allOf"].([]any)
	}
	if !hasUnion {
		return
	}
	_ = union
	wrapper := make(map[string]any, len(schema))
	for key, value := range schema {
		wrapper[key] = value
	}
	clear(schema)
	schema["type"] = "object"
	schema["properties"] = map[string]any{"value": wrapper}
	schema["required"] = []any{"value"}
	schema["additionalProperties"] = false
}

func normalizeOpenAITupleSchema(schema map[string]any, path string, depth int, rewrites *[]schemaPointerRewrite, references *[]map[string]any) error {
	if depth > 128 {
		return fmt.Errorf("tool input schema exceeds maximum nesting depth")
	}
	if len(path) > 4096 {
		return fmt.Errorf("tool input schema exceeds maximum pointer depth")
	}
	if len(*rewrites) >= 1000 {
		return fmt.Errorf("tool input schema exceeds maximum tuple positions")
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
	maxItems, bounded := schema["maxItems"].(float64)
	if !bounded {
		// OpenAI-compatible tool schemas commonly reject tuple-form items. An
		// unbounded tuple remains valid if every later element matches the last
		// prefix schema, so model the open tail explicitly.
		schema["items"] = map[string]any{}
	} else if maxItems > float64(len(items)) {
		switch additional := schema["additionalItems"].(type) {
		case map[string]any:
			schema["items"] = additional
			*rewrites = append(*rewrites, schemaPointerRewrite{
				path + "/additionalItems",
				path + "/items",
			})
		case bool:
			if !additional {
				// Boolean schemas are not portable in tool parameters. Refuse
				// extra positions instead of emitting false.
				schema["maxItems"] = float64(len(items))
			}
			schema["items"] = map[string]any{}
		default:
			schema["items"] = map[string]any{}
		}
	} else {
		delete(schema, "items")
	}
	delete(schema, "additionalItems")
	schema["prefixItems"] = items
	for i := range items {
		*rewrites = append(*rewrites, schemaPointerRewrite{
			fmt.Sprintf("%s/items/%d", path, i),
			fmt.Sprintf("%s/prefixItems/%d", path, i),
		})
	}
	if len(*rewrites) > 1000 {
		return fmt.Errorf("tool input schema exceeds maximum tuple positions")
	}
	return nil
}
