package config

import "fmt"

func (c Config) validateAIStorageConfig() error {
	s := c.AIStorage
	if !c.Features.AIStorage {
		if s != Defaults().AIStorage {
			return fmt.Errorf("ai_storage settings require ai_storage feature")
		}
		return nil
	}
	if !c.Features.AIPolicy {
		return fmt.Errorf("ai_storage requires ai_policy")
	}
	if err := secretRef("ai_storage.grant_public_key_env", s.GrantPublicKeyEnv, true); err != nil {
		return err
	}
	if s.Issuer == "" || s.Audience == "" || s.Audience == c.Security.Audience || s.Audience == c.AIPolicy.Audience {
		return fmt.Errorf("ai_storage requires a distinct nonempty grant audience")
	}
	for _, field := range []struct {
		name       string
		value, max int
	}{
		{"grant_ttl_seconds", s.GrantTTLSeconds, 900}, {"max_resources", s.MaxResources, 1024}, {"max_documents_per_resource", s.MaxDocuments, 16384}, {"max_item_bytes", s.MaxItemBytes, 16 << 20}, {"max_vector_dimensions", s.MaxVectorDimensions, 4096}, {"max_results", s.MaxResults, 100}, {"max_graph_depth", s.MaxGraphDepth, 8},
	} {
		if field.value < 1 || field.value > field.max {
			return fmt.Errorf("ai_storage.%s must be 1..%d", field.name, field.max)
		}
	}
	return nil
}
