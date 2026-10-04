//go:build qg_openai || qg_anthropic

package modules

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
)

// The deliberately small schema vocabulary is enforced locally; unsupported
// JSON Schema constraints fail closed rather than pretending full support.
func aiCheckArguments(schema map[string]any, raw json.RawMessage) error {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return errors.New("invalid tool arguments")
	}
	if _, ok := value.(map[string]any); !ok {
		return errors.New("tool arguments must be an object")
	}
	if schema == nil {
		if len(value.(map[string]any)) > 0 {
			return errors.New("schema required for tool arguments")
		}
		return nil
	}
	return aiCheckSchema(schema, value, 0)
}
func aiCheckSchema(schema map[string]any, value any, depth int) error {
	if depth > 8 {
		return errors.New("tool schema depth exceeded")
	}
	for key := range schema {
		switch key {
		case "type", "properties", "required", "additionalProperties", "items", "enum", "description", "title":
		default:
			return errors.New("unsupported tool schema constraint")
		}
	}
	typeName, _ := schema["type"].(string)
	switch typeName {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return errors.New("tool argument type mismatch")
		}
		properties := map[string]any{}
		if p, ok := schema["properties"].(map[string]any); ok {
			properties = p
		}
		if required, ok := schema["required"]; ok {
			rv := reflect.ValueOf(required)
			if rv.Kind() != reflect.Slice {
				return errors.New("invalid required schema")
			}
			for i := 0; i < rv.Len(); i++ {
				name, ok := rv.Index(i).Interface().(string)
				if !ok {
					return errors.New("invalid required schema")
				}
				if _, exists := object[name]; !exists {
					return errors.New("required tool argument missing")
				}
			}
		}
		for name, v := range object {
			p, exists := properties[name]
			if !exists {
				allowed, ok := schema["additionalProperties"].(bool)
				if !ok || !allowed {
					return errors.New("unknown tool argument")
				}
				continue
			}
			child, ok := p.(map[string]any)
			if !ok {
				return errors.New("invalid property schema")
			}
			if e := aiCheckSchema(child, v, depth+1); e != nil {
				return e
			}
		}
	case "array":
		array, ok := value.([]any)
		if !ok || len(array) > 1024 {
			return errors.New("invalid tool array")
		}
		items, ok := schema["items"].(map[string]any)
		if !ok {
			return errors.New("array items schema required")
		}
		for _, v := range array {
			if e := aiCheckSchema(items, v, depth+1); e != nil {
				return e
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			return errors.New("tool argument type mismatch")
		}
	case "number":
		if _, ok := value.(float64); !ok {
			return errors.New("tool argument type mismatch")
		}
	case "integer":
		n, ok := value.(float64)
		if !ok || n != float64(int64(n)) {
			return errors.New("tool argument type mismatch")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return errors.New("tool argument type mismatch")
		}
	case "null":
		if value != nil {
			return errors.New("tool argument type mismatch")
		}
	default:
		return errors.New("tool schema requires a supported type")
	}
	if enumeration, ok := schema["enum"]; ok {
		rv := reflect.ValueOf(enumeration)
		if rv.Kind() != reflect.Slice {
			return errors.New("invalid enum schema")
		}
		found := false
		encoded, _ := json.Marshal(value)
		for i := 0; i < rv.Len(); i++ {
			candidate, _ := json.Marshal(rv.Index(i).Interface())
			if bytes.Equal(encoded, candidate) {
				found = true
			}
		}
		if !found {
			return errors.New("tool argument outside enum")
		}
	}
	return nil
}
