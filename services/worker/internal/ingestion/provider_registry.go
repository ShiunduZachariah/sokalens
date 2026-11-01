package ingestion

import (
	"fmt"
	"strings"
)

type ProviderAdapter struct {
	Provider string
	Queues   map[string]map[string]JobHandler
}

type ProviderRegistry struct {
	handlersByQueue map[string]map[string]JobHandler
}

func NewProviderRegistry(adapters ...ProviderAdapter) (ProviderRegistry, error) {
	registry := ProviderRegistry{handlersByQueue: map[string]map[string]JobHandler{}}
	for _, adapter := range adapters {
		provider := strings.TrimSpace(adapter.Provider)
		if provider == "" {
			return ProviderRegistry{}, fmt.Errorf("provider adapter name is required")
		}
		for queue, handlers := range adapter.Queues {
			queue = strings.TrimSpace(queue)
			if queue == "" {
				return ProviderRegistry{}, fmt.Errorf("provider adapter %q has empty queue", provider)
			}
			if registry.handlersByQueue[queue] == nil {
				registry.handlersByQueue[queue] = map[string]JobHandler{}
			}
			for jobKey, handler := range handlers {
				jobKey = strings.TrimSpace(jobKey)
				if jobKey == "" {
					return ProviderRegistry{}, fmt.Errorf("provider adapter %q has empty job key", provider)
				}
				if handler == nil {
					return ProviderRegistry{}, fmt.Errorf("provider adapter %q job %q has nil handler", provider, jobKey)
				}
				if registry.handlersByQueue[queue][jobKey] != nil {
					return ProviderRegistry{}, fmt.Errorf("duplicate provider job key %q in queue %q", jobKey, queue)
				}
				registry.handlersByQueue[queue][jobKey] = handler
			}
		}
	}
	return registry, nil
}

func (r ProviderRegistry) Handlers(queue string) map[string]JobHandler {
	handlers := r.handlersByQueue[queue]
	if len(handlers) == 0 {
		return map[string]JobHandler{}
	}
	copied := make(map[string]JobHandler, len(handlers))
	for key, handler := range handlers {
		copied[key] = handler
	}
	return copied
}
