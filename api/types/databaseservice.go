/*
Copyright 2022 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package types

import (
	"github.com/gogo/protobuf/proto"
	"github.com/gravitational/teleport/api/utils"
	"github.com/gravitational/trace"
)

// DatabaseService represents a database service (agent).
type DatabaseService interface {
	// ResourceWithLabels provides common resource methods.
	ResourceWithLabels

	// GetResourceMatchers returns the resource matchers of the DatabaseService.
	GetResourceMatchers() []Labels

	// SetResourceMatchers sets the resource matchers.
	SetResourceMatchers(resourceMatchers []Labels)

	// Copy returns a copy of this database service object.
	Copy() DatabaseService
}

// NewDatabaseServiceV1 creates a new database service instance.
func NewDatabaseServiceV1(meta Metadata, spec DatabaseServiceSpecV1) (*DatabaseServiceV1, error) {
	s := &DatabaseServiceV1{
		ResourceHeader: ResourceHeader{
			Metadata: meta,
		},
		Spec: spec,
	}

	if err := s.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}
	return s, nil
}

func (s *DatabaseServiceV1) setStaticFields() {
	s.Kind = KindDatabaseService
	s.Version = V1
}

// CheckAndSetDefaults checks and sets default values for any missing fields.
func (s *DatabaseServiceV1) CheckAndSetDefaults() error {
	s.setStaticFields()

	return trace.Wrap(s.ResourceHeader.CheckAndSetDefaults())
}

// GetResourceMatchers returns the resource matchers of the DatabaseService.
func (s *DatabaseServiceV1) GetResourceMatchers() []Labels {
	return s.Spec.ResourceMatchers
}

// SetResourceMatchers sets the resource matchers.
func (s *DatabaseServiceV1) SetResourceMatchers(resourceMatchers []Labels) {
	s.Spec.ResourceMatchers = resourceMatchers
}

// GetAllLabels returns the resources labels.
func (s *DatabaseServiceV1) GetAllLabels() map[string]string {
	return s.Metadata.Labels
}

// GetStaticLabels returns the windows desktop static labels.
func (s *DatabaseServiceV1) GetStaticLabels() map[string]string {
	return s.Metadata.Labels
}

// MatchSearch goes through select field values and tries to
// match against the list of search values.
func (s *DatabaseServiceV1) MatchSearch(values []string) bool {
	fieldVals := append(utils.MapToStrings(s.GetAllLabels()), s.GetName())
	return MatchSearch(fieldVals, values, nil)
}

// Origin returns the origin value of the resource.
func (d *DatabaseServiceV1) Origin() string {
	return d.Metadata.Labels[OriginLabel]
}

// SetOrigin sets the origin value of the resource.
func (d *DatabaseServiceV1) SetOrigin(o string) {
	d.Metadata.Labels[OriginLabel] = o
}

// SetStaticLabels sets the windows desktop static labels.
func (d *DatabaseServiceV1) SetStaticLabels(sl map[string]string) {
	d.Metadata.Labels = sl
}

// Copy returns a copy of this database service object.
func (s *DatabaseServiceV1) Copy() DatabaseService {
	return proto.Clone(s).(*DatabaseServiceV1)
}
