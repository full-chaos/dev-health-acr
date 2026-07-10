package memory

import (
	"context"
	"sync"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type AuditStore struct {
	mu     sync.RWMutex
	events []storage.AuditEvent
}

func NewAuditStore() *AuditStore { return &AuditStore{} }

func (s *AuditStore) Record(_ context.Context, event storage.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	event.Metadata = cloneMetadata(event.Metadata)
	s.events = append(s.events, event)
	return nil
}

func (s *AuditStore) Events() []storage.AuditEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]storage.AuditEvent, len(s.events))
	for index, event := range s.events {
		event.Metadata = cloneMetadata(event.Metadata)
		result[index] = event
	}
	return result
}

func cloneMetadata(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}
