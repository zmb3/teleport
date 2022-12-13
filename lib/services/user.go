/*
Copyright 2019 Gravitational, Inc.

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

package services

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/gravitational/trace"

	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/utils"
)

// ValidateUser validates the User and sets default values
func ValidateUser(u types.User) error {
	if err := u.CheckAndSetDefaults(); err != nil {
		return trace.Wrap(err)
	}
	if localAuth := u.GetLocalAuth(); localAuth != nil {
		if err := ValidateLocalAuthSecrets(localAuth); err != nil {
			return trace.Wrap(err)
		}
	}
	return nil
}

// ValidateUserRoles checks that all the roles in the user exist
func ValidateUserRoles(ctx context.Context, u types.User, roleGetter RoleGetter) error {
	for _, role := range u.GetRoles() {
		if _, err := roleGetter.GetRole(ctx, role); err != nil {
			return trace.Wrap(err)
		}
	}
	return nil
}

// UsersEquals checks if the users are equal
func UsersEquals(u types.User, other types.User) bool {
	return cmp.Equal(u, other,
		cmpopts.IgnoreFields(types.Metadata{}, "ID"),
		cmpopts.SortSlices(func(a, b *types.MFADevice) bool {
			return a.Metadata.Name < b.Metadata.Name
		}),
	)
}

// LoginAttempt represents successful or unsuccessful attempt for user to login
type LoginAttempt struct {
	// Time is time of the attempt
	Time time.Time `json:"time"`
	// Success indicates whether attempt was successful
	Success bool `json:"bool"`
}

// Check checks parameters
func (la *LoginAttempt) Check() error {
	if la.Time.IsZero() {
		return trace.BadParameter("missing parameter time")
	}
	return nil
}

// UnmarshalUser unmarshals the User resource from JSON.
func UnmarshalUser(bytes []byte, opts ...MarshalOption) (types.User, error) {
	var h types.ResourceHeader
	err := json.Unmarshal(bytes, &h)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	cfg, err := CollectOptions(opts)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	switch h.Version {
	case types.V2:
		var u types.UserV2
		if err := utils.FastUnmarshal(bytes, &u); err != nil {
			return nil, trace.BadParameter(err.Error())
		}

		if err := ValidateUser(&u); err != nil {
			return nil, trace.Wrap(err)
		}
		if cfg.ID != 0 {
			u.SetResourceID(cfg.ID)
		}
		if !cfg.Expires.IsZero() {
			u.SetExpiry(cfg.Expires)
		}

		return &u, nil
	}
	return nil, trace.BadParameter("user resource version %v is not supported", h.Version)
}

// MarshalUser marshals the User resource to JSON.
func MarshalUser(user types.User, opts ...MarshalOption) ([]byte, error) {
	if err := ValidateUser(user); err != nil {
		return nil, trace.Wrap(err)
	}

	cfg, err := CollectOptions(opts)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	switch user := user.(type) {
	case *types.UserV2:
		if !cfg.PreserveResourceID {
			// avoid modifying the original object
			// to prevent unexpected data races
			copy := *user
			copy.SetResourceID(0)
			user = &copy
		}
		return utils.FastMarshal(user)
	default:
		return nil, trace.BadParameter("unrecognized user version %T", user)
	}
}
