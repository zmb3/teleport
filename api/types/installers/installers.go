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

package installers

import (
	_ "embed"

	"github.com/zmb3/teleport/api/types"
)

//go:embed installer.sh.tmpl
var defaultInstallScript string

// InstallerScriptName is the name of the by default populated, EC2
// installer script
const InstallerScriptName = "default-installer"

// DefaultInstaller represents a the default installer script provided
// by teleport
var DefaultInstaller = types.MustNewInstallerV1(InstallerScriptName, defaultInstallScript)

// Template is used to fill proxy address and version information into
// the installer script
type Template struct {
	// PublicProxyAddr is public address of the proxy
	PublicProxyAddr string
	// MajorVersion is the major version of the Teleport auth node
	MajorVersion string
}
