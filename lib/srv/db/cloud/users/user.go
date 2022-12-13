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

package users

import (
	"context"
	"time"

	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"github.com/sirupsen/logrus"

	"github.com/zmb3/teleport/lib/srv/db/secrets"
)

// User represents a managed cloud database user.
type User interface {
	// GetID returns a globally unique ID for the user.
	GetID() string
	// GetDatabaseUsername returns in-database username for the user.
	GetDatabaseUsername() string
	// Setup preforms any setup necessary like creating password secret.
	Setup(ctx context.Context) error
	// Teardown performs any teardown necessary like deleting password secret.
	Teardown(ctx context.Context) error
	// GetPassword returns the password used for database login.
	GetPassword(ctx context.Context) (string, error)
	// RotatePassword rotates user's password.
	RotatePassword(ctx context.Context) error
}

// cloudResource manages the underlying cloud resource of the database
// user.
type cloudResource interface {
	// ModifyUserPassword updates user passwords of the cloud resource.
	ModifyUserPassword(ctx context.Context, oldPassword, newPassword string) error
}

// baseUser is a base implementation of User.
type baseUser struct {
	// secrets is a secret store helper.
	secrets secrets.Secrets
	// secretKey is a globally unique key path for the secret.
	secretKey string
	// secretTTL is the lifetime of each version of the secret password.
	secretTTL time.Duration
	// databaseUsername is the in-database username.
	databaseUsername string
	// maxPasswordLength is the size of random password to be generated.
	maxPasswordLength int
	// usePreviousPasswordForLogin uses previous version of the password for
	// database login. If false, the current version of the password is used.
	usePreviousPasswordForLogin bool
	// cloudResource is used to manage the underlying cloud resource.
	cloudResource cloudResource
	// clock is used to control time.
	clock clockwork.Clock
	// log is the logrus field logger.
	log logrus.FieldLogger
}

// CheckAndSetDefaults validates the Resource and sets any empty fields to
// default values.
func (u *baseUser) CheckAndSetDefaults() error {
	if u.secrets == nil {
		return trace.BadParameter("missing secrets")
	}
	if u.secretKey == "" {
		return trace.BadParameter("missing secret key")
	}
	if u.secretTTL == 0 {
		return trace.BadParameter("missing secret TTL")
	}
	if u.databaseUsername == "" {
		return trace.BadParameter("missing username")
	}
	if u.maxPasswordLength <= 0 {
		return trace.BadParameter("invalid max password length")
	}
	if u.cloudResource == nil {
		return trace.BadParameter("missing cloud resource")
	}
	if u.clock == nil {
		u.clock = clockwork.NewRealClock()
	}
	if u.log == nil {
		u.log = logrus.WithField(trace.Component, "clouduser")
	}
	return nil
}

// String returns baseUser's string description.
func (u *baseUser) String() string {
	return u.GetID()
}

// GetID returns a globally unique ID for the user.
func (u *baseUser) GetID() string {
	return u.secretKey
}

// GetDatabaseUsername returns in-database username for the user.
func (u *baseUser) GetDatabaseUsername() string {
	return u.databaseUsername
}

// Setup preforms any setup necessary like creating password secret.
func (u *baseUser) Setup(ctx context.Context) error {
	u.log.Debugf("Setting up user %v", u)

	newPassword, err := genRandomPassword(u.maxPasswordLength)
	if err != nil {
		return trace.Wrap(err)
	}

	err = u.secrets.CreateOrUpdate(ctx, u.secretKey, newPassword)
	if err != nil {
		if trace.IsAlreadyExists(err) {
			return nil
		}
		return trace.Wrap(err)
	}

	return trace.Wrap(u.cloudResource.ModifyUserPassword(ctx, "", newPassword))
}

// Teardown performs any teardown necessary like deleting password secret.
func (u *baseUser) Teardown(ctx context.Context) error {
	u.log.Debugf("Tearing down user %v", u)

	err := trace.Wrap(u.secrets.Delete(ctx, u.secretKey))
	if err != nil {
		// The secret may have been removed by another agent already.
		if trace.IsNotFound(err) {
			return nil
		}
		return trace.Wrap(err)
	}
	return nil
}

// GetPassword returns the password used for database login.
func (u *baseUser) GetPassword(ctx context.Context) (string, error) {
	// Use current/latest version for login.
	if !u.usePreviousPasswordForLogin {
		return u.getPassword(ctx, secrets.CurrentVersion)
	}

	// User previous version for login.
	password, err := u.getPassword(ctx, secrets.PreviousVersion)
	if err != nil {
		// Rare case check when there is only one version at the moment. Do a
		// second get to use the current version.
		//
		// It is also possible someone else has deleted the secret completely.
		// In that case the next rotate password will handle it by recreating
		// the secret.
		if trace.IsNotFound(err) {
			return u.getPassword(ctx, secrets.CurrentVersion)
		}
		return "", trace.Wrap(err)
	}
	return password, nil
}

// getPassword returns the password used for database login.
func (u *baseUser) getPassword(ctx context.Context, version string) (string, error) {
	value, err := u.secrets.GetValue(ctx, u.secretKey, version)
	if err != nil {
		return "", trace.Wrap(err)
	}
	return value.Value, nil
}

// RotatePassword rotates user's password.
func (u *baseUser) RotatePassword(ctx context.Context) error {
	currentValue, err := u.secrets.GetValue(ctx, u.secretKey, secrets.CurrentVersion)
	if err != nil {
		// Rare case check when someone else has deleted the secret.
		if trace.IsNotFound(err) {
			return trace.Wrap(u.Setup(ctx))
		}

		return trace.Wrap(err)
	}

	// The password is up-to-date. Nothing to do.
	expiresAt := currentValue.CreatedAt.Add(u.secretTTL)
	if u.clock.Now().Before(expiresAt) {
		return nil
	}

	u.log.Debugf("Updating password for user %v", u)
	newPassword, err := genRandomPassword(u.maxPasswordLength)
	if err != nil {
		return trace.Wrap(err)
	}

	// PutValue uses currentValue.Version to perform a test-and-set operation
	// so in case of racing agents getting here at the same time, only one will
	// succeed.
	err = u.secrets.PutValue(ctx, u.secretKey, newPassword, currentValue.Version)
	if err != nil {
		return trace.Wrap(err)
	}

	return trace.Wrap(u.cloudResource.ModifyUserPassword(ctx, currentValue.Value, newPassword))
}
