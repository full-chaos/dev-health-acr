package memory

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type AuditStore struct {
	mu          sync.RWMutex
	lifecycleMu *sync.RWMutex
	lifecycle   *credentialStore
	events      []storage.AuditEvent
}

func NewAuditStore() *AuditStore { return &AuditStore{} }

func (s *AuditStore) Record(ctx context.Context, event storage.AuditEvent) error {
	if s == nil {
		return storage.ErrInvalidCredentialLifecycle
	}
	if storage.IsNil(ctx) {
		return storage.ErrInvalidCredentialInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if storage.IsCredentialLifecycleAuditAction(event.Action) {
		return storage.ErrInvalidCredentialInput
	}
	s.mu.RLock()
	lifecycleMu := s.lifecycleMu
	s.mu.RUnlock()
	if lifecycleMu != nil {
		lifecycleMu.Lock()
		defer lifecycleMu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.recordLocked(event)
}

func (s *AuditStore) Events() []storage.AuditEvent {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	lifecycleMu := s.lifecycleMu
	s.mu.RUnlock()
	if lifecycleMu != nil {
		lifecycleMu.RLock()
		defer lifecycleMu.RUnlock()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]storage.AuditEvent, len(s.events))
	for index, event := range s.events {
		metadata, err := cloneMetadata(event.Metadata)
		if err != nil {
			return nil
		}
		event.Metadata = metadata
		result[index] = event
	}
	return result
}

func (s *AuditStore) bindLifecycle(store *credentialStore) error {
	if store == nil {
		return errors.New("credential store is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lifecycle != nil {
		return storage.ErrConflict
	}
	s.lifecycle = store
	s.lifecycleMu = &store.mu
	return nil
}

func (s *AuditStore) recordLocked(event storage.AuditEvent) error {
	metadata, err := cloneMetadata(event.Metadata)
	if err != nil {
		return err
	}
	event.Metadata = metadata
	s.events = append(s.events, event)
	return nil
}

func cloneMetadata(value map[string]any) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	cloned, err := cloneMetadataValue(reflect.ValueOf(value), make(map[visit]bool))
	if err != nil {
		return nil, err
	}
	return cloned.Convert(reflect.TypeFor[map[string]any]()).Interface().(map[string]any), nil
}

type visit struct {
	typ reflect.Type
	ptr uintptr
}

var timeType = reflect.TypeFor[time.Time]()

func cloneMetadataValue(value reflect.Value, stack map[visit]bool) (reflect.Value, error) {
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		cloned, err := cloneMetadataValue(value.Elem(), stack)
		if err != nil {
			return reflect.Value{}, err
		}
		result := reflect.New(value.Type()).Elem()
		result.Set(cloned)
		return result, nil
	}
	if value.Type() == timeType {
		return value, nil
	}
	switch value.Kind() {
	case reflect.Bool, reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr, reflect.Float32, reflect.Float64:
		return value, nil
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		key := visit{value.Type(), value.Pointer()}
		if stack[key] {
			return reflect.Value{}, fmt.Errorf("%w: cyclic value", storage.ErrInvalidAuditMetadata)
		}
		stack[key] = true
		defer delete(stack, key)
		item, err := cloneMetadataValue(value.Elem(), stack)
		if err != nil {
			return reflect.Value{}, err
		}
		copy := reflect.New(value.Type().Elem())
		copy.Elem().Set(item)
		if copy.Type() == value.Type() {
			return copy, nil
		}
		if copy.Type().ConvertibleTo(value.Type()) {
			return copy.Convert(value.Type()), nil
		}
		return reflect.Value{}, fmt.Errorf("%w: unsupported pointer type", storage.ErrInvalidAuditMetadata)
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		key := visit{value.Type(), value.Pointer()}
		if stack[key] {
			return reflect.Value{}, fmt.Errorf("%w: cyclic value", storage.ErrInvalidAuditMetadata)
		}
		stack[key] = true
		defer delete(stack, key)
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := range value.Len() {
			item, err := cloneMetadataValue(value.Index(i), stack)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Index(i).Set(item)
		}
		return result, nil
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		if value.Type().Key().Kind() != reflect.String {
			return reflect.Value{}, fmt.Errorf("%w: map key must be string", storage.ErrInvalidAuditMetadata)
		}
		key := visit{value.Type(), value.Pointer()}
		if stack[key] {
			return reflect.Value{}, fmt.Errorf("%w: cyclic value", storage.ErrInvalidAuditMetadata)
		}
		stack[key] = true
		defer delete(stack, key)
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			item, err := cloneMetadataValue(iter.Value(), stack)
			if err != nil {
				return reflect.Value{}, err
			}
			result.SetMapIndex(iter.Key(), item)
		}
		return result, nil
	default:
		return reflect.Value{}, fmt.Errorf("%w: unsupported %s", storage.ErrInvalidAuditMetadata, value.Type())
	}
}
