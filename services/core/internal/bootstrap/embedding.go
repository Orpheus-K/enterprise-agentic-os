package bootstrap

import "agentos.local/core/internal/model"

// Construction does not contact the provider or enable retrieval. The conversation
// consumer must bind its space and execution revision before using this client.
func openEmbedder(config *model.EmbeddingConfig) (*model.Embedder, error) {
	if config == nil {
		return nil, nil
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	var key string
	if config.APIKeyEnv != "" {
		var err error
		key, err = secret(config.APIKeyEnv)
		if err != nil {
			return nil, err
		}
	}
	return model.NewEmbedder(*config, key)
}
