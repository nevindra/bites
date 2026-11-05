package wire

import "reflect"

// JSONSchema is the shape an app declares for a type that writes or reads its own JSON, with
// WithJSONSchema (ADR 0076). The codec cannot see inside such a type, so the app says what it
// is once, and the API document states it wherever the type appears.
type JSONSchema struct {
	// Type is the JSON type: "string", "number", "integer", "boolean", "object" or "array", or
	// "any" for a value that is free-form JSON, described as {}.
	Type string
	// Format is the OpenAPI format of a "string", "number" or "integer" type.
	Format string
	// Nullable says the type's own methods can write null (or read it), so the document
	// allows null beside Type.
	Nullable bool
}

// JSONSchemas maps a type to the schema its author gave it with WithJSONSchema.
type JSONSchemas map[reflect.Type]JSONSchema

// JSONTypes lists the Type values a JSONSchema may have.
var JSONTypes = []string{"string", "number", "integer", "boolean", "object", "array", "any"}
