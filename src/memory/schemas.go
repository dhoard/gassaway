// Copyright (c) 2026-present Douglas Hoard. All rights reserved.
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// This file is the published argument contract: one JSON Schema per tool,
// authored as an artifact rather than inferred from the Go handler types.
//
// Three properties of the reference memory server are deliberate and are not
// visible in a struct tag, which is why these are written out:
//
//   - The root object and the item objects are open. A caller that sends a
//     field this server does not model is ignored instead of refused, and a
//     payload that is missing a required property is reported as such. A closed
//     object makes the schema validator complain about additional properties
//     first, which hides the actionable message.
//   - Property names, wording and required lists are the reference's, so a
//     prompt or client written against it transfers unchanged.
//
// Both the argument schema and the result schema are authored here. Inference
// from the Go types cannot express the reference's result contract: a Go slice
// infers as ["null","array"], while the reference declares "array", and the
// handlers always send an array. A client that validates a result against the
// published schema therefore has to be told what the server actually sends.
//
// The literal text is the capture itself, in testdata/reference-tools.json:
// surface_test.go fails if a schema here or on the wire drifts from it, in either
// direction. Nothing is projected out, so no argument the reference accepts is
// missing here and none is advertised that the server does not implement.
package memory

import (
	"encoding/json"
	"fmt"
)

// mustSchema decodes an authored schema. A malformed literal is a programming
// error, not a runtime condition: it would otherwise reach a client as an
// unusable tool definition, so it is refused at start-up (and witnessed by a
// test) instead.
func mustSchema(raw string) map[string]any {
	var schema map[string]any
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		panic(fmt.Errorf("memory: authored tool schema is not valid JSON: %w", err))
	}
	return schema
}

// createEntitiesSchema is the published argument schema of create_entities.
const createEntitiesSchemaJSON = `{
  "type": "object",
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "properties": {
    "entities": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "name": {
            "type": "string",
            "description": "The name of the entity"
          },
          "entityType": {
            "type": "string",
            "description": "The type of the entity"
          },
          "observations": {
            "type": "array",
            "items": {
              "type": "string"
            },
            "description": "An array of observation contents associated with the entity"
          }
        },
        "required": [
          "name",
          "entityType",
          "observations"
        ]
      }
    }
  },
  "required": [
    "entities"
  ]
}`

// createEntitiesResultSchema is the published result schema of create_entities.
const createEntitiesResultSchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "entities": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "name": {
            "type": "string",
            "description": "The name of the entity"
          },
          "entityType": {
            "type": "string",
            "description": "The type of the entity"
          },
          "observations": {
            "type": "array",
            "items": {
              "type": "string"
            },
            "description": "An array of observation contents associated with the entity"
          }
        },
        "required": [
          "name",
          "entityType",
          "observations"
        ],
        "additionalProperties": false
      }
    },
    "skipped": {
      "type": "array",
      "items": {
        "type": "string"
      }
    }
  },
  "required": [
    "entities"
  ],
  "additionalProperties": false
}`

// createRelationsSchema is the published argument schema of create_relations.
const createRelationsSchemaJSON = `{
  "type": "object",
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "properties": {
    "relations": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "from": {
            "type": "string",
            "description": "The name of the entity where the relation starts"
          },
          "to": {
            "type": "string",
            "description": "The name of the entity where the relation ends"
          },
          "relationType": {
            "type": "string",
            "description": "The type of the relation"
          }
        },
        "required": [
          "from",
          "to",
          "relationType"
        ]
      }
    }
  },
  "required": [
    "relations"
  ]
}`

// createRelationsResultSchema is the published result schema of create_relations.
const createRelationsResultSchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "relations": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "from": {
            "type": "string",
            "description": "The name of the entity where the relation starts"
          },
          "to": {
            "type": "string",
            "description": "The name of the entity where the relation ends"
          },
          "relationType": {
            "type": "string",
            "description": "The type of the relation"
          }
        },
        "required": [
          "from",
          "to",
          "relationType"
        ],
        "additionalProperties": false
      }
    }
  },
  "required": [
    "relations"
  ],
  "additionalProperties": false
}`

// addObservationsSchema is the published argument schema of add_observations.
const addObservationsSchemaJSON = `{
  "type": "object",
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "properties": {
    "observations": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "entityName": {
            "type": "string",
            "description": "The name of the entity to add the observations to"
          },
          "contents": {
            "type": "array",
            "items": {
              "type": "string"
            },
            "description": "An array of observation contents to add"
          }
        },
        "required": [
          "entityName",
          "contents"
        ]
      }
    }
  },
  "required": [
    "observations"
  ]
}`

// addObservationsResultSchema is the published result schema of add_observations.
const addObservationsResultSchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "results": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "entityName": {
            "type": "string"
          },
          "addedObservations": {
            "type": "array",
            "items": {
              "type": "string"
            }
          }
        },
        "required": [
          "entityName",
          "addedObservations"
        ],
        "additionalProperties": false
      }
    }
  },
  "required": [
    "results"
  ],
  "additionalProperties": false
}`

// deleteEntitiesSchema is the published argument schema of delete_entities.
const deleteEntitiesSchemaJSON = `{
  "type": "object",
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "properties": {
    "entityNames": {
      "type": "array",
      "items": {
        "type": "string"
      },
      "description": "An array of entity names to delete"
    }
  },
  "required": [
    "entityNames"
  ]
}`

// deleteEntitiesResultSchema is the published result schema of delete_entities.
const deleteEntitiesResultSchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "success": {
      "type": "boolean"
    },
    "message": {
      "type": "string"
    }
  },
  "required": [
    "success",
    "message"
  ],
  "additionalProperties": false
}`

// deleteObservationsSchema is the published argument schema of delete_observations.
const deleteObservationsSchemaJSON = `{
  "type": "object",
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "properties": {
    "deletions": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "entityName": {
            "type": "string",
            "description": "The name of the entity containing the observations"
          },
          "observations": {
            "type": "array",
            "items": {
              "type": "string"
            },
            "description": "An array of observations to delete"
          }
        },
        "required": [
          "entityName",
          "observations"
        ]
      }
    }
  },
  "required": [
    "deletions"
  ]
}`

// deleteObservationsResultSchema is the published result schema of delete_observations.
const deleteObservationsResultSchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "success": {
      "type": "boolean"
    },
    "message": {
      "type": "string"
    }
  },
  "required": [
    "success",
    "message"
  ],
  "additionalProperties": false
}`

// deleteRelationsSchema is the published argument schema of delete_relations.
const deleteRelationsSchemaJSON = `{
  "type": "object",
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "properties": {
    "relations": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "from": {
            "type": "string",
            "description": "The name of the entity where the relation starts"
          },
          "to": {
            "type": "string",
            "description": "The name of the entity where the relation ends"
          },
          "relationType": {
            "type": "string",
            "description": "The type of the relation"
          }
        },
        "required": [
          "from",
          "to",
          "relationType"
        ]
      },
      "description": "An array of relations to delete"
    }
  },
  "required": [
    "relations"
  ]
}`

// deleteRelationsResultSchema is the published result schema of delete_relations.
const deleteRelationsResultSchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "success": {
      "type": "boolean"
    },
    "message": {
      "type": "string"
    }
  },
  "required": [
    "success",
    "message"
  ],
  "additionalProperties": false
}`

// readGraphSchema is the published argument schema of read_graph.
const readGraphSchemaJSON = `{
  "type": "object",
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "properties": {
    "entityTypes": {
      "description": "Return only entities whose entityType is one of these (exact match), and only the relations that touch one of them. Omit to return every entity.",
      "minItems": 1,
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "observationLimit": {
      "description": "Return at most this many observations per entity (the first ones, in the order they were added) and add each entity's total as observationCount. Use 0 for names, types and counts only: a cheap overview of a large graph, after which open_nodes reads the entities you need in full. Omit to return every observation.",
      "type": "integer",
      "minimum": 0,
      "maximum": 9007199254740991
    }
  }
}`

// readGraphResultSchema is the published result schema of read_graph.
const readGraphResultSchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "entities": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "name": {
            "type": "string",
            "description": "The name of the entity"
          },
          "entityType": {
            "type": "string",
            "description": "The type of the entity"
          },
          "observations": {
            "type": "array",
            "items": {
              "type": "string"
            },
            "description": "An array of observation contents associated with the entity"
          },
          "observationCount": {
            "description": "How many observations the entity holds in total; present only when observationLimit was given",
            "type": "integer",
            "minimum": 0,
            "maximum": 9007199254740991
          }
        },
        "required": [
          "name",
          "entityType",
          "observations"
        ],
        "additionalProperties": false
      }
    },
    "relations": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "from": {
            "type": "string",
            "description": "The name of the entity where the relation starts"
          },
          "to": {
            "type": "string",
            "description": "The name of the entity where the relation ends"
          },
          "relationType": {
            "type": "string",
            "description": "The type of the relation"
          }
        },
        "required": [
          "from",
          "to",
          "relationType"
        ],
        "additionalProperties": false
      }
    }
  },
  "required": [
    "entities",
    "relations"
  ],
  "additionalProperties": false
}`

// searchNodesSchema is the published argument schema of search_nodes.
const searchNodesSchemaJSON = `{
  "type": "object",
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "properties": {
    "query": {
      "type": "string",
      "maxLength": 2048,
      "description": "The search query to match against entity names, types, and observation content"
    },
    "limit": {
      "description": "Return at most this many matching entities (the first matches, in stored order), with the relations that touch them. Omit to return every match.",
      "type": "integer",
      "minimum": 1,
      "maximum": 9007199254740991
    },
    "observationLimit": {
      "description": "Return at most this many observations per entity (the first ones, in the order they were added) and add each entity's total as observationCount. Use 0 for names, types and counts only: a cheap overview of a large graph, after which open_nodes reads the entities you need in full. Omit to return every observation.",
      "type": "integer",
      "minimum": 0,
      "maximum": 9007199254740991
    }
  },
  "required": [
    "query"
  ]
}`

// searchNodesResultSchema is the published result schema of search_nodes.
const searchNodesResultSchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "entities": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "name": {
            "type": "string",
            "description": "The name of the entity"
          },
          "entityType": {
            "type": "string",
            "description": "The type of the entity"
          },
          "observations": {
            "type": "array",
            "items": {
              "type": "string"
            },
            "description": "An array of observation contents associated with the entity"
          },
          "observationCount": {
            "description": "How many observations the entity holds in total; present only when observationLimit was given",
            "type": "integer",
            "minimum": 0,
            "maximum": 9007199254740991
          }
        },
        "required": [
          "name",
          "entityType",
          "observations"
        ],
        "additionalProperties": false
      }
    },
    "relations": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "from": {
            "type": "string",
            "description": "The name of the entity where the relation starts"
          },
          "to": {
            "type": "string",
            "description": "The name of the entity where the relation ends"
          },
          "relationType": {
            "type": "string",
            "description": "The type of the relation"
          }
        },
        "required": [
          "from",
          "to",
          "relationType"
        ],
        "additionalProperties": false
      }
    }
  },
  "required": [
    "entities",
    "relations"
  ],
  "additionalProperties": false
}`

// openNodesSchema is the published argument schema of open_nodes.
const openNodesSchemaJSON = `{
  "type": "object",
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "properties": {
    "names": {
      "type": "array",
      "items": {
        "type": "string"
      },
      "description": "An array of entity names to retrieve"
    },
    "observationLimit": {
      "description": "Return at most this many observations per entity (the first ones, in the order they were added) and add each entity's total as observationCount. Use 0 for names, types and counts only: a cheap overview of a large graph, after which open_nodes reads the entities you need in full. Omit to return every observation.",
      "type": "integer",
      "minimum": 0,
      "maximum": 9007199254740991
    }
  },
  "required": [
    "names"
  ]
}`

// openNodesResultSchema is the published result schema of open_nodes.
const openNodesResultSchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "entities": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "name": {
            "type": "string",
            "description": "The name of the entity"
          },
          "entityType": {
            "type": "string",
            "description": "The type of the entity"
          },
          "observations": {
            "type": "array",
            "items": {
              "type": "string"
            },
            "description": "An array of observation contents associated with the entity"
          },
          "observationCount": {
            "description": "How many observations the entity holds in total; present only when observationLimit was given",
            "type": "integer",
            "minimum": 0,
            "maximum": 9007199254740991
          }
        },
        "required": [
          "name",
          "entityType",
          "observations"
        ],
        "additionalProperties": false
      }
    },
    "relations": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "from": {
            "type": "string",
            "description": "The name of the entity where the relation starts"
          },
          "to": {
            "type": "string",
            "description": "The name of the entity where the relation ends"
          },
          "relationType": {
            "type": "string",
            "description": "The type of the relation"
          }
        },
        "required": [
          "from",
          "to",
          "relationType"
        ],
        "additionalProperties": false
      }
    }
  },
  "required": [
    "entities",
    "relations"
  ],
  "additionalProperties": false
}`

var (
	createEntitiesSchema           = mustSchema(createEntitiesSchemaJSON)
	createEntitiesResultSchema     = mustSchema(createEntitiesResultSchemaJSON)
	createRelationsSchema          = mustSchema(createRelationsSchemaJSON)
	createRelationsResultSchema    = mustSchema(createRelationsResultSchemaJSON)
	addObservationsSchema          = mustSchema(addObservationsSchemaJSON)
	addObservationsResultSchema    = mustSchema(addObservationsResultSchemaJSON)
	deleteEntitiesSchema           = mustSchema(deleteEntitiesSchemaJSON)
	deleteEntitiesResultSchema     = mustSchema(deleteEntitiesResultSchemaJSON)
	deleteObservationsSchema       = mustSchema(deleteObservationsSchemaJSON)
	deleteObservationsResultSchema = mustSchema(deleteObservationsResultSchemaJSON)
	deleteRelationsSchema          = mustSchema(deleteRelationsSchemaJSON)
	deleteRelationsResultSchema    = mustSchema(deleteRelationsResultSchemaJSON)
	readGraphSchema                = mustSchema(readGraphSchemaJSON)
	readGraphResultSchema          = mustSchema(readGraphResultSchemaJSON)
	searchNodesSchema              = mustSchema(searchNodesSchemaJSON)
	searchNodesResultSchema        = mustSchema(searchNodesResultSchemaJSON)
	openNodesSchema                = mustSchema(openNodesSchemaJSON)
	openNodesResultSchema          = mustSchema(openNodesResultSchemaJSON)
)
