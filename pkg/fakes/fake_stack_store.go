package fakes

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/containerd/typeurl"
	gogotypes "github.com/gogo/protobuf/types"

	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/errdefs"

	"github.com/docker/stacks/pkg/interfaces"
	"github.com/docker/stacks/pkg/types"
)

// FakeStackStore stores stacks
type FakeStackStore struct {
	stacks map[string]*interfaces.SnapshotStack
	sync.RWMutex
	curID       int
	labelErrors map[string]error
	keyPrefix   string
}

func init() {
	typeurl.Register(&types.StackSpec{}, "github.com/docker/stacks/StackSpec")
}

// CopyStackSpec duplicates the types.StackSpec
func CopyStackSpec(spec types.StackSpec) (types.StackSpec, error) {
	var payload *gogotypes.Any
	var err error
	payload, err = typeurl.MarshalAny(&spec)
	if err != nil {
		return types.StackSpec{}, err
	}
	iface, err := typeurl.UnmarshalAny(payload)
	if err != nil {
		return types.StackSpec{}, err
	}
	return *iface.(*types.StackSpec), nil
}

func fakeConstructStack(snapshotStack *interfaces.SnapshotStack) types.Stack {

	stackSpec, _ := CopyStackSpec(snapshotStack.CurrentSpec)

	stack := types.Stack{
		ID:   snapshotStack.ID,
		Meta: snapshotStack.Meta,
		Spec: stackSpec,
	}
	return stack
}

// NewFakeStackStore creates a new FakeStackStore
func NewFakeStackStore() *FakeStackStore {
	return &FakeStackStore{
		stacks: make(map[string]*interfaces.SnapshotStack),
		// Don't start from ID 0, to catch any uninitialized types.
		curID:       1,
		labelErrors: map[string]error{},
	}
}

var errNotFound = errdefs.NotFound(errors.New("stack not found"))

// AddStack adds a stack to the store.
func (s *FakeStackStore) AddStack(stackSpec types.StackSpec) (string, error) {
	s.Lock()
	defer s.Unlock()

	stackSpec, _ = CopyStackSpec(stackSpec)

	snapshot := &interfaces.SnapshotStack{
		SnapshotResource: interfaces.SnapshotResource{
			ID: fmt.Sprintf("%s|%04d", s.keyPrefix, s.curID),
			Meta: swarm.Meta{
				Version: swarm.Version{
					Index: 1,
				},
			},
			Name: stackSpec.Annotations.Name,
		},
		CurrentSpec: stackSpec,
	}
	s.stacks[snapshot.ID] = snapshot

	s.curID++
	return snapshot.ID, s.causeAnError(nil, "AddStack", stackSpec)
}

func (s *FakeStackStore) getSnapshotStack(id string) (*interfaces.SnapshotStack, error) {
	snapshot, ok := s.stacks[id]
	if !ok {
		return nil, errNotFound
	}

	return snapshot, nil
}

func (s *FakeStackStore) getStack(id string) (types.Stack, error) {
	snapshot, err := s.getSnapshotStack(id)
	if err != nil {
		return types.Stack{}, errNotFound
	}
	return fakeConstructStack(snapshot), nil
}

// UpdateStack updates the stack in the store.
func (s *FakeStackStore) UpdateStack(id string, stackSpec types.StackSpec, version uint64) error {
	s.Lock()
	defer s.Unlock()

	stackSpec, _ = CopyStackSpec(stackSpec)

	existing, err := s.getSnapshotStack(id)
	if err != nil {
		return errNotFound
	}

	if existing.Version.Index != version {
		return fmt.Errorf("update out of sequence")
	}
	existing.Version.Index++
	stackID := existing.ID

	for _, service := range stackSpec.Services {
		if service.Annotations.Labels == nil {
			service.Annotations.Labels = map[string]string{}
		}
		service.Annotations.Labels[types.StackLabel] = stackID
	}
	for _, config := range stackSpec.Configs {
		if config.Annotations.Labels == nil {
			config.Annotations.Labels = map[string]string{}
		}
		config.Annotations.Labels[types.StackLabel] = stackID
	}
	for _, secret := range stackSpec.Secrets {
		if secret.Annotations.Labels == nil {
			secret.Annotations.Labels = map[string]string{}
		}
		secret.Annotations.Labels[types.StackLabel] = stackID
	}
	for _, network := range stackSpec.Networks {
		if network.Labels == nil {
			network.Labels = map[string]string{}
		}
		network.Labels[types.StackLabel] = stackID
	}
	existing.CurrentSpec = stackSpec
	s.stacks[id] = existing
	return s.causeAnError(nil, "UpdateStack", stackSpec)
}

// UpdateSnapshotStack updates the snapshot in the store.
func (s *FakeStackStore) UpdateSnapshotStack(id string, snapshot interfaces.SnapshotStack, version uint64) error {
	s.Lock()
	defer s.Unlock()

	existing, err := s.getSnapshotStack(id)
	if err != nil {
		return errNotFound
	}

	if existing.Version.Index != version {
		return fmt.Errorf("update out of sequence")
	}
	existing.Version.Index++

	// No accidental or sly changes to the StackSpec are permitted
	existing.Services = snapshot.Services
	existing.Configs = snapshot.Configs
	existing.Secrets = snapshot.Secrets
	existing.Networks = snapshot.Networks

	s.stacks[id] = existing
	return s.causeAnError(nil, "UpdateSnapshotStack", existing.CurrentSpec)
}

// DeleteStack removes a stack from the store.
func (s *FakeStackStore) DeleteStack(id string) error {
	s.Lock()
	defer s.Unlock()
	stack, err := s.getStack(id)
	delete(s.stacks, id)
	return s.causeAnError(err, "DeleteStack", stack.Spec)
}

// GetStack retrieves a single stack from the store.
func (s *FakeStackStore) GetStack(id string) (types.Stack, error) {
	s.RLock()
	defer s.RUnlock()
	stack, err := s.getStack(id)
	return stack, s.causeAnError(err, "GetStack", stack.Spec)
}

// GetSnapshotStack retrieves a single stack from the store.
func (s *FakeStackStore) GetSnapshotStack(id string) (*interfaces.SnapshotStack, error) {
	s.RLock()
	defer s.RUnlock()
	snapshot, err := s.getSnapshotStack(id)
	return snapshot, s.causeAnError(err, "GetSnapshotStack", snapshot.CurrentSpec)
}

// ListStacks returns all known stacks from the store.
func (s *FakeStackStore) ListStacks() ([]types.Stack, error) {
	s.RLock()
	defer s.RUnlock()
	stacks := []types.Stack{}
	for _, key := range s.SortedIDs() {
		snapshot := s.stacks[key]
		stacks = append(stacks, fakeConstructStack(snapshot))
	}
	return stacks, nil
}

func (s *FakeStackStore) causeAnError(err error, operation string, spec types.StackSpec) error {
	if err != nil {
		return err
	}

	key := s.constructErrorMark(operation)
	errorName, ok := spec.Annotations.Labels[key]
	if !ok {
		key := s.constructErrorMark("")
		errorName, ok = spec.Annotations.Labels[key]
		if !ok {
			return nil
		}
	}

	return s.labelErrors[errorName]
}

// SpecifyError associates an error to a key
func (s *FakeStackStore) SpecifyError(errorKey string, err error) {
	s.labelErrors[errorKey] = err
}

// SpecifyKeyPrefix provides prefix to generated ID's
func (s *FakeStackStore) SpecifyKeyPrefix(keyPrefix string) {
	s.keyPrefix = keyPrefix
}

func (s *FakeStackStore) constructErrorMark(operation string) string {
	if len(operation) == 0 {
		return s.keyPrefix + ".storeError"
	}
	return s.keyPrefix + "." + operation + ".storeError"
}

// MarkInputForError mark StackSpec with potential errors
func (s *FakeStackStore) MarkInputForError(errorKey string, input interface{}, ops ...string) {
	spec := input.(*types.StackSpec)
	if spec.Annotations.Labels == nil {
		spec.Annotations.Labels = make(map[string]string)
	}
	if len(ops) == 0 {
		key := s.constructErrorMark("")
		spec.Annotations.Labels[key] = errorKey
	} else {
		for _, operation := range ops {
			key := s.constructErrorMark(operation)
			spec.Annotations.Labels[key] = errorKey
		}
	}
}

// InternalAddStack adds types.Stack to storage without preconditions
func (s *FakeStackStore) InternalAddStack(id string, snapshot *interfaces.SnapshotStack) {
	s.stacks[id] = snapshot
}

// InternalGetStack retrieves types.Stack or nil from storage without preconditions
func (s *FakeStackStore) InternalGetStack(id string) *interfaces.SnapshotStack {
	stack, ok := s.stacks[id]
	if !ok {
		return nil
	}
	return stack
}

// InternalQueryStacks retrieves all types.Stack from storage while applying a transform
func (s *FakeStackStore) InternalQueryStacks(transform func(*interfaces.SnapshotStack) interface{}) []interface{} {
	result := make([]interface{}, 0)

	for _, key := range s.SortedIDs() {
		item := s.InternalGetStack(key)
		if transform == nil {
			result = append(result, item)
		} else {
			view := transform(item)
			if view != nil {
				result = append(result, view)
			}
		}
	}
	return result
}

// InternalDeleteStack removes types.Stack from storage without preconditions
func (s *FakeStackStore) InternalDeleteStack(id string) *interfaces.SnapshotStack {
	snapshot, ok := s.stacks[id]
	if !ok {
		return nil
	}
	delete(s.stacks, id)
	return snapshot
}

// SortedIDs returns sorted Stack IDs
func (s *FakeStackStore) SortedIDs() []string {
	result := []string{}
	for key, value := range s.stacks {
		if value != nil {
			result = append(result, key)
		}
	}
	sort.Strings(result)
	return result
}
