/**
 * Copyright 2022 Gravitational, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package types

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/gogo/protobuf/proto"
	"github.com/gravitational/teleport/api/defaults"
	"github.com/gravitational/trace"
)

// IsStrictKebabCase checks if the given string meets a fairly strict definition of
// kebab case (no dots, dashes, caps, etc). Useful for strings that might need to be
// included in filenames.
var IsStrictKebabCase = regexp.MustCompile(`^[a-z0-9\-]+$`).MatchString

func (e *ExecScript) Check() error {
	if !IsStrictKebabCase(e.Type) {
		// type name is used in a filename, so we need to be strict
		// about its allowed characters.
		return trace.BadParameter("invalid type %q in exec-script message", e.Type)
	}

	if e.Script == "" {
		return trace.BadParameter("missing required field 'script' in exec-script message")
	}

	for name := range e.Env {
		if !isValidEnvVarName(name) {
			return trace.BadParameter("invalid env var name %q in exec-script message", name)
		}
	}

	for _, name := range e.EnvPassthrough {
		if !isValidEnvVarName(name) {
			return trace.BadParameter("invalid env passthrough var name %q in exec-script message", name)
		}
	}

	return nil
}

// VersionControlInstallerType is the type of a version control installer.
type VersionControlInstallerType string

const (
	// InstallerTypeNone indicates that type was not specified. The meaning of this is
	// context-dependent (e.g. a filter treats InstallerTypeNone as 'match all').
	InstallerTypeNone VersionControlInstallerType = ""

	// InstallerType is the local script installer type.
	InstallerTypeLocalScript VersionControlInstallerType = "local-script"
)

func (t VersionControlInstallerType) Check() error {
	switch t {
	case InstallerTypeNone:
	case InstallerTypeLocalScript:
	default:
		return trace.BadParameter("unknown version control installer type %q", t)
	}
	return nil
}

func (i *LocalScriptInstallerV1) GetInstallerType() VersionControlInstallerType {
	return InstallerTypeLocalScript
}

// VersionDirectiveState represents the current state of a version directive.
type VersionDirectiveState string

const (
	// DirectiveStateNone indicates that state was not specified. The meaning of this is
	// context-dependent (e.g. a filter treats DirectiveStateNone as 'match all').
	DirectiveStateNone VersionDirectiveState = ""

	// DirectiveStateDraft indicates that the version directive is in a fully mutable and potentially
	// incomplete/provisional state.
	DirectiveStateDraft VersionDirectiveState = "draft"

	// DirectiveStatePending indicates that the version directive's parameters have been frozen. It is
	// assumed complete/finalized, and *may* be promoted to ACTIVE in the future.
	DirectiveStatePending VersionDirectiveState = "pending"

	// DirectiveStateActive indicates that the version directive occupies the 'active' slot. Note that
	// this does not necessarily imply that the directive is being enforced. Directives
	// can be disabled or poisoned, and are subject to scheduling.
	DirectiveStateActive VersionDirectiveState = "active"
)

func (s VersionDirectiveState) Check() error {
	switch s {
	case DirectiveStateNone:
	case DirectiveStateDraft:
	case DirectiveStatePending:
	case DirectiveStateActive:
	default:
		return trace.BadParameter("unexpected version directive state %q", s)
	}
	return nil
}

// Match checks if a given installer matches this filter.
func (f VersionControlInstallerFilter) Match(installer VersionControlInstaller) bool {
	if f.Type != InstallerTypeNone && f.Type != installer.GetInstallerType() {
		return false
	}

	if f.Name != "" && f.Name != installer.GetName() {
		return false
	}

	if f.Nonce != 0 && f.Nonce != installer.GetNonce() {
		return false
	}

	return true
}

// Check verifies sanity of filter parameters.
func (f VersionControlInstallerFilter) Check() error {
	if err := f.Type.Check(); err != nil {
		return trace.Wrap(err)
	}

	if f.Name != "" && f.Type == InstallerTypeNone {
		return trace.BadParameter("cannot resolve installer %q without an installer type")
	}

	return nil
}

// VersionControlInstaller abstracts over the common methods of all version conrol installers.
type VersionControlInstaller interface {
	Resource

	// GetInstallerType gets the type of the installer.
	GetInstallerType() VersionControlInstallerType

	// GetNonce gets the nonce of the installer.
	GetNonce() uint64
}

// LocalScriptInstaller is used by the version control system to install
// new teleport versions via user-provided scripts.
type LocalScriptInstaller interface {
	VersionControlInstaller

	// BaseInstallMsg builds the base exec message for the install.sh script. The returned
	// value requires additional configuration to be valid (id, variable interpolation, etc...).
	BaseInstallMsg() ExecScript

	// BaseRestartMsg builds the base exec message for the restart.sh script. The returned
	// value requires additional configuration to be valid (id, variable interpolation, etc...).
	BaseRestartMsg() ExecScript

	// Clone performs a deep copy.
	Clone() LocalScriptInstaller
}

// NewLocalScriptInstaller constructs a new LocalScriptInstaller from the provided spec.
func NewLocalScriptInstaller(name string, spec LocalScriptInstallerSpecV1) (LocalScriptInstaller, error) {
	installer := &LocalScriptInstallerV1{
		ResourceHeader: ResourceHeader{
			Metadata: Metadata{
				Name: name,
			},
		},
		Spec: spec,
	}

	if err := installer.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	return installer, nil
}

func (i *LocalScriptInstallerV1) GetNonce() uint64 {
	return i.Spec.Nonce
}

// WithIncrementedNonce returns a shallow copy with an incremented nonce.
func (i *LocalScriptInstallerV1) WithIncrementedNonce() LocalScriptInstallerV1 {
	shallowCopy := *i
	shallowCopy.Spec.Nonce++
	return shallowCopy
}

func (i *LocalScriptInstallerV1) CheckAndSetDefaults() error {
	i.setStaticFields()

	if err := i.ResourceHeader.CheckAndSetDefaults(); err != nil {
		return trace.Wrap(err)
	}

	if i.Version != V1 {
		return trace.BadParameter("unsupported local script installer version: %s", i.Version)
	}

	if i.Kind != KindLocalScriptInstaller {
		return trace.BadParameter("unexpected resource kind: %q (expected %s)", i.Kind, KindLocalScriptInstaller)
	}

	if i.Metadata.Namespace != "" && i.Metadata.Namespace != defaults.Namespace {
		return trace.BadParameter("invalid namespace %q (namespaces are deprecated)", i.Metadata.Namespace)
	}

	if i.Spec.InstallScript == "" {
		return trace.BadParameter("missing required field 'install.sh' in local script installer")
	}

	if i.Spec.RestartScript == "" {
		return trace.BadParameter("missing required field 'restart.sh' in local script installer")
	}

	for name := range i.Spec.Env {
		if !isValidEnvVarName(name) {
			return trace.BadParameter("invalid env var name %q in local script installer", name)
		}
	}

	for _, name := range i.Spec.EnvPassthrough {
		if !isValidEnvVarName(name) {
			return trace.BadParameter("invalid env passthrough var name %q in local script installer", name)
		}
	}

	if i.Spec.Shell != "" {
		// verify shell directive w/ optional shebang-style arg
		parts := strings.SplitN(strings.TrimSpace(i.Spec.Shell), " ", 2)
		if !filepath.IsAbs(parts[0]) {
			return trace.BadParameter("non-absolute shell path %q in local script installer", parts[0])
		}

		for _, arg := range parts[1:] {
			// some shebang impls bundle all space separated args after the executable
			// path into a single argument, and some allow for multiple args. the former
			// is more common, but the latter is generally regarded as superior. we sidestep
			// the issue for now by simply disallowing additional spaces. this will allow
			// us to adopt either behavior in the future w/o breaking user-facing comatibility
			// (though care will need to be taken to ensure auth<->node compat).
			for _, c := range arg {
				if unicode.IsSpace(c) {
					return trace.BadParameter("invalid argument %q for shell of local script installer", arg)
				}
			}
		}
	}

	return nil
}

// isValidEnvVarName checks if the given name is valid for use in scipt installers.
func isValidEnvVarName(name string) bool {
	if name == "" {
		return false
	}

	for _, c := range name {
		if c == '=' || unicode.IsSpace(c) {
			return false
		}
	}

	return true
}

func (i *LocalScriptInstallerV1) setStaticFields() {
	if i.Version == "" {
		i.Version = V1
	}

	if i.Kind == "" {
		i.Kind = KindLocalScriptInstaller
	}
}

func (i *LocalScriptInstallerV1) BaseInstallMsg() ExecScript {
	msg := i.baseExecMsg()
	msg.Script = i.Spec.InstallScript
	return msg
}

func (i *LocalScriptInstallerV1) BaseRestartMsg() ExecScript {
	msg := i.baseExecMsg()
	msg.Script = i.Spec.RestartScript
	return msg
}

func (i *LocalScriptInstallerV1) baseExecMsg() ExecScript {
	return ExecScript{
		Env:            i.Spec.Env,
		EnvPassthrough: i.Spec.EnvPassthrough,
		Shell:          i.Spec.Shell,
	}
}

func (i *LocalScriptInstallerV1) Clone() LocalScriptInstaller {
	return proto.Clone(i).(*LocalScriptInstallerV1)
}
