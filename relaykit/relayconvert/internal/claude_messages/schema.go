package claudemessages

import openaischema "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/shared/openaischema"

func inputSchemaToOpenAIParameters(input any) (any, error) {
	return openaischema.InputSchemaToParameters(input)
}
