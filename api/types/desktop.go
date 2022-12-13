/*
Copyright 2021 Gravitational, Inc.

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
	"sort"

	"github.com/gravitational/trace"

	"github.com/zmb3/teleport/api/utils"
)

// WindowsDesktopService represents a Windows desktop service instance.
type WindowsDesktopService interface {
	// ResourceWithLabels provides common resource methods.
	ResourceWithLabels
	// GetAddr returns the network address of this service.
	GetAddr() string
	// GetVersion returns the teleport binary version of this service.
	GetTeleportVersion() string
	// GetHostname returns the hostname of this service
	GetHostname() string
	// ProxiedService provides common methods for a proxied service.
	ProxiedService
}

type WindowsDesktopServices []WindowsDesktopService

// AsResources returns windows desktops as type resources with labels.
func (s WindowsDesktopServices) AsResources() []ResourceWithLabels {
	resources := make([]ResourceWithLabels, 0, len(s))
	for _, server := range s {
		resources = append(resources, ResourceWithLabels(server))
	}
	return resources
}

var _ WindowsDesktopService = &WindowsDesktopServiceV3{}

// NewWindowsDesktopServiceV3 creates a new WindowsDesktopServiceV3 resource.
func NewWindowsDesktopServiceV3(meta Metadata, spec WindowsDesktopServiceSpecV3) (*WindowsDesktopServiceV3, error) {
	s := &WindowsDesktopServiceV3{
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

func (s *WindowsDesktopServiceV3) setStaticFields() {
	s.Kind = KindWindowsDesktopService
	s.Version = V3
}

// CheckAndSetDefaults checks and sets default values for any missing fields.
func (s *WindowsDesktopServiceV3) CheckAndSetDefaults() error {
	if s.Spec.Addr == "" {
		return trace.BadParameter("WindowsDesktopServiceV3.Spec missing Addr field")
	}
	if s.Spec.TeleportVersion == "" {
		return trace.BadParameter("WindowsDesktopServiceV3.Spec missing TeleportVersion field")
	}

	s.setStaticFields()
	if err := s.ResourceHeader.CheckAndSetDefaults(); err != nil {
		return trace.Wrap(err)
	}
	return nil
}

// GetAddr returns the network address of this service.
func (s *WindowsDesktopServiceV3) GetAddr() string {
	return s.Spec.Addr
}

// GetTeleportVersion returns the teleport binary version of this service.
func (s *WindowsDesktopServiceV3) GetTeleportVersion() string {
	return s.Spec.TeleportVersion
}

// Origin returns the origin value of the resource.
func (s *WindowsDesktopServiceV3) Origin() string {
	return s.Metadata.Origin()
}

// SetOrigin sets the origin value of the resource.
func (s *WindowsDesktopServiceV3) SetOrigin(origin string) {
	s.Metadata.SetOrigin(origin)
}

// GetProxyID returns a list of proxy ids this server is connected to.
func (s *WindowsDesktopServiceV3) GetProxyIDs() []string {
	return s.Spec.ProxyIDs
}

// SetProxyID sets the proxy ids this server is connected to.
func (s *WindowsDesktopServiceV3) SetProxyIDs(proxyIDs []string) {
	s.Spec.ProxyIDs = proxyIDs
}

// GetAllLabels returns the resources labels.
func (s *WindowsDesktopServiceV3) GetAllLabels() map[string]string {
	return s.Metadata.Labels
}

// GetStaticLabels returns the windows desktop static labels.
func (s *WindowsDesktopServiceV3) GetStaticLabels() map[string]string {
	return s.Metadata.Labels
}

// SetStaticLabels sets the windows desktop static labels.
func (s *WindowsDesktopServiceV3) SetStaticLabels(sl map[string]string) {
	s.Metadata.Labels = sl
}

// GetHostname returns the windows hostname of this service.
func (s *WindowsDesktopServiceV3) GetHostname() string {
	return s.Spec.Hostname
}

// MatchSearch goes through select field values and tries to
// match against the list of search values.
func (s *WindowsDesktopServiceV3) MatchSearch(values []string) bool {
	fieldVals := append(utils.MapToStrings(s.GetAllLabels()), s.GetName(), s.GetHostname())
	return MatchSearch(fieldVals, values, nil)
}

// WindowsDesktop represents a Windows desktop host.
type WindowsDesktop interface {
	// ResourceWithLabels provides common resource methods.
	ResourceWithLabels
	// GetAddr returns the network address of this host.
	GetAddr() string
	// LabelsString returns all labels as a string.
	LabelsString() string
	// GetDomain returns the ActiveDirectory domain of this host.
	GetDomain() string
	// GetHostID returns the ID of the Windows Desktop Service reporting the desktop.
	GetHostID() string
}

var _ WindowsDesktop = &WindowsDesktopV3{}

// NewWindowsDesktopV3 creates a new WindowsDesktopV3 resource.
func NewWindowsDesktopV3(name string, labels map[string]string, spec WindowsDesktopSpecV3) (*WindowsDesktopV3, error) {
	d := &WindowsDesktopV3{
		ResourceHeader: ResourceHeader{
			Metadata: Metadata{
				Name:   name,
				Labels: labels,
			},
		},
		Spec: spec,
	}
	if err := d.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}
	return d, nil
}

func (d *WindowsDesktopV3) setStaticFields() {
	d.Kind = KindWindowsDesktop
	d.Version = V3
}

// CheckAndSetDefaults checks and sets default values for any missing fields.
func (d *WindowsDesktopV3) CheckAndSetDefaults() error {
	if d.Spec.Addr == "" {
		return trace.BadParameter("WindowsDesktopV3.Spec missing Addr field")
	}

	d.setStaticFields()
	if err := d.ResourceHeader.CheckAndSetDefaults(); err != nil {
		return trace.Wrap(err)
	}
	return nil
}

// GetAddr returns the network address of this host.
func (d *WindowsDesktopV3) GetAddr() string {
	return d.Spec.Addr
}

// GetHostID returns the HostID for the associated desktop service.
func (d *WindowsDesktopV3) GetHostID() string {
	return d.Spec.HostID
}

// GetAllLabels returns combined static and dynamic labels.
func (d *WindowsDesktopV3) GetAllLabels() map[string]string {
	// TODO(zmb3): add dynamic labels when running in agent mode
	return CombineLabels(d.Metadata.Labels, nil)
}

// GetStaticLabels returns the windows desktop static labels.
func (d *WindowsDesktopV3) GetStaticLabels() map[string]string {
	return d.Metadata.Labels
}

// SetStaticLabels sets the windows desktop static labels.
func (d *WindowsDesktopV3) SetStaticLabels(sl map[string]string) {
	d.Metadata.Labels = sl
}

// LabelsString returns all desktop labels as a string.
func (d *WindowsDesktopV3) LabelsString() string {
	return LabelsAsString(d.Metadata.Labels, nil)
}

// GetDomain returns the Active Directory domain of this host.
func (d *WindowsDesktopV3) GetDomain() string {
	return d.Spec.Domain
}

// Origin returns the origin value of the resource.
func (d *WindowsDesktopV3) Origin() string {
	return d.Metadata.Labels[OriginLabel]
}

// SetOrigin sets the origin value of the resource.
func (d *WindowsDesktopV3) SetOrigin(o string) {
	d.Metadata.Labels[OriginLabel] = o
}

// MatchSearch goes through select field values and tries to
// match against the list of search values.
func (d *WindowsDesktopV3) MatchSearch(values []string) bool {
	fieldVals := append(utils.MapToStrings(d.GetAllLabels()), d.GetName(), d.GetAddr())
	return MatchSearch(fieldVals, values, nil)
}

// DeduplicateDesktops deduplicates desktops by name.
func DeduplicateDesktops(desktops []WindowsDesktop) (result []WindowsDesktop) {
	seen := make(map[string]struct{})
	for _, desktop := range desktops {
		if _, ok := seen[desktop.GetName()]; ok {
			continue
		}
		seen[desktop.GetName()] = struct{}{}
		result = append(result, desktop)
	}
	return result
}

// Match checks if a given desktop request matches this filter.
func (f *WindowsDesktopFilter) Match(req WindowsDesktop) bool {
	if f.HostID != "" && req.GetHostID() != f.HostID {
		return false
	}
	if f.Name != "" && req.GetName() != f.Name {
		return false
	}
	return true
}

// WindowsDesktops represents a list of windows desktops.
type WindowsDesktops []WindowsDesktop

// Len returns the slice length.
func (s WindowsDesktops) Len() int { return len(s) }

// Less compares desktops by name and host ID.
func (s WindowsDesktops) Less(i, j int) bool {
	switch {
	case s[i].GetName() < s[j].GetName():
		return true
	case s[i].GetName() > s[j].GetName():
		return false
	default:
		return s[i].GetHostID() < s[j].GetHostID()
	}
}

// Swap swaps two windows desktops.
func (s WindowsDesktops) Swap(i, j int) { s[i], s[j] = s[j], s[i] }

// SortByCustom custom sorts by given sort criteria.
func (s WindowsDesktops) SortByCustom(sortBy SortBy) error {
	if sortBy.Field == "" {
		return nil
	}

	isDesc := sortBy.IsDesc
	switch sortBy.Field {
	case ResourceMetadataName:
		sort.SliceStable(s, func(i, j int) bool {
			return stringCompare(s[i].GetName(), s[j].GetName(), isDesc)
		})
	case ResourceSpecAddr:
		sort.SliceStable(s, func(i, j int) bool {
			return stringCompare(s[i].GetAddr(), s[j].GetAddr(), isDesc)
		})
	default:
		return trace.NotImplemented("sorting by field %q for resource %q is not supported", sortBy.Field, KindWindowsDesktop)
	}

	return nil
}

// AsResources returns windows desktops as type resources with labels.
func (s WindowsDesktops) AsResources() []ResourceWithLabels {
	resources := make([]ResourceWithLabels, 0, len(s))
	for _, server := range s {
		resources = append(resources, ResourceWithLabels(server))
	}
	return resources
}

// GetFieldVals returns list of select field values.
func (s WindowsDesktops) GetFieldVals(field string) ([]string, error) {
	vals := make([]string, 0, len(s))
	switch field {
	case ResourceMetadataName:
		for _, server := range s {
			vals = append(vals, server.GetName())
		}
	case ResourceSpecAddr:
		for _, server := range s {
			vals = append(vals, server.GetAddr())
		}
	default:
		return nil, trace.NotImplemented("getting field %q for resource %q is not supported", field, KindWindowsDesktop)
	}

	return vals, nil
}

// ListWindowsDesktopsResponse is a response type to ListWindowsDesktops.
type ListWindowsDesktopsResponse struct {
	Desktops []WindowsDesktop
	NextKey  string
}

// ListWindowsDesktopsRequest is a request type to ListWindowsDesktops.
type ListWindowsDesktopsRequest struct {
	WindowsDesktopFilter
	Limit                         int
	StartKey, PredicateExpression string
	Labels                        map[string]string
	SearchKeywords                []string
}

// ListWindowsDesktopServicesResponse is a response type to ListWindowsDesktopServices.
type ListWindowsDesktopServicesResponse struct {
	DesktopServices []WindowsDesktopService
	NextKey         string
}

// ListWindowsDesktopServicesRequest is a request type to ListWindowsDesktopServices.
type ListWindowsDesktopServicesRequest struct {
	Limit                         int
	StartKey, PredicateExpression string
	Labels                        map[string]string
	SearchKeywords                []string
}
