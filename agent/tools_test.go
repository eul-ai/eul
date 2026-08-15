package agent

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
)

func TestJSONSchemaMarshalOrdersPropertiesByRequired(t *testing.T) {
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]JSONSchema{
			"optionalB": {Type: "boolean"},
			"second": {
				Type: "object",
				Properties: map[string]JSONSchema{
					"nestedSecond": {Type: "string"},
					"nestedFirst":  {Type: "string"},
				},
				Required: []string{"nestedFirst", "nestedSecond"},
			},
			"first":     {Type: "string"},
			"optionalA": {Type: "integer"},
		},
		Required: []string{"first", "second"},
	}

	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}

	properties := schemaProperties(t, encoded)
	if order := jsonObjectOrder(t, properties); !slices.Equal(order, []string{"first", "second", "optionalA", "optionalB"}) {
		t.Fatalf("property order = %v", order)
	}

	var values map[string]json.RawMessage
	if err := json.Unmarshal(properties, &values); err != nil {
		t.Fatal(err)
	}
	nested := schemaProperties(t, values["second"])
	if order := jsonObjectOrder(t, nested); !slices.Equal(order, []string{"nestedFirst", "nestedSecond"}) {
		t.Fatalf("nested property order = %v", order)
	}
}

func schemaProperties(t *testing.T, encoded []byte) json.RawMessage {
	t.Helper()

	var schema struct {
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	return schema.Properties
}

func jsonObjectOrder(t *testing.T, encoded []byte) []string {
	t.Helper()

	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if _, err := decoder.Token(); err != nil {
		t.Fatal(err)
	}

	var order []string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			t.Fatal(err)
		}
		name, ok := token.(string)
		if !ok {
			t.Fatalf("object key = %v", token)
		}
		order = append(order, name)

		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		t.Fatal(err)
	}
	return order
}
