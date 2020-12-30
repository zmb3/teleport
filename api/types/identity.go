/*
Copyright 2020 Gravitational, Inc.

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
	"fmt"

	"github.com/gravitational/trace"
)

// String returns debug friendly representation of this identity
func (i *ExternalIdentity) String() string {
	return fmt.Sprintf("OIDCIdentity(connectorID=%v, username=%v)", i.ConnectorID, i.Username)
}

// Equals returns true if this identity equals to passed one
func (i *ExternalIdentity) Equals(other *ExternalIdentity) bool {
	return i.ConnectorID == other.ConnectorID && i.Username == other.Username
}

// Check returns nil if all parameters are great, err otherwise
func (i *ExternalIdentity) Check() error {
	if i.ConnectorID == "" {
		return trace.BadParameter("ConnectorID: missing value")
	}
	if i.Username == "" {
		return trace.BadParameter("Username: missing username")
	}
	return nil
}

// ExternalIdentitySchema is JSON schema for ExternalIdentity
const ExternalIdentitySchema = `{
	"type": "object",
	"additionalProperties": false,
	"properties": {
	   "connector_id": {"type": "string"},
	   "username": {"type": "string"}
	 }
  }`
