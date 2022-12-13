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

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/elasticache"
	"github.com/aws/aws-sdk-go/service/elasticache/elasticacheiface"
	"github.com/gravitational/trace"

	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/api/utils"
	libaws "github.com/zmb3/teleport/lib/cloud/aws"
	libsecrets "github.com/zmb3/teleport/lib/srv/db/secrets"
	libutils "github.com/zmb3/teleport/lib/utils"
)

// elastiCacheFetcher is a fetcher for discovering ElastiCache users.
type elastiCacheFetcher struct {
	cfg   Config
	cache *libutils.FnCache
}

// newElastiCacheFetcher creates a new instance of ElastiCache fetcher.
func newElastiCacheFetcher(cfg Config) (Fetcher, error) {
	if err := cfg.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	// cache is used to cache cloud resources fetched from cloud APIs to avoid
	// making the same call repeatedly in a short time.
	cache, err := libutils.NewFnCache(libutils.FnCacheConfig{
		TTL:   cfg.Interval / 2, // Make sure cache expires at next interval.
		Clock: cfg.Clock,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return &elastiCacheFetcher{
		cfg:   cfg,
		cache: cache,
	}, nil
}

// GetType returns the database type of the fetcher. Implements Fetcher.
func (f *elastiCacheFetcher) GetType() string {
	return types.DatabaseTypeElastiCache
}

// FetchDatabaseUsers fetches users for provided database. Implements Fetcher.
func (f *elastiCacheFetcher) FetchDatabaseUsers(ctx context.Context, database types.Database) ([]User, error) {
	if len(database.GetAWS().ElastiCache.UserGroupIDs) == 0 {
		return nil, nil
	}

	client, err := f.cfg.Clients.GetAWSElastiCacheClient(database.GetAWS().Region)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	secrets, err := newSecretStore(database, f.cfg.Clients)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	users := []User{}
	for _, userGroupID := range database.GetAWS().ElastiCache.UserGroupIDs {
		managedUsers, err := f.getManagedUsersForGroup(ctx, database.GetAWS().Region, userGroupID, client)
		if err != nil {
			return nil, trace.Wrap(err)
		}

		for _, managedUser := range managedUsers {
			user, err := f.createUser(managedUser, client, secrets)
			if err != nil {
				return nil, trace.Wrap(err)
			}

			users = append(users, user)
		}
	}
	return users, nil
}

// getManagedUsersForGroup returns all managed users for specified user group ID.
func (f *elastiCacheFetcher) getManagedUsersForGroup(ctx context.Context, region, userGroupID string, client elasticacheiface.ElastiCacheAPI) ([]*elasticache.User, error) {
	allUsers, err := f.getUsersForRegion(ctx, region, client)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	managedUsers := []*elasticache.User{}
	for _, user := range allUsers {
		// Match user group ID.
		if !utils.SliceContainsStr(aws.StringValueSlice(user.UserGroupIds), userGroupID) {
			continue
		}

		// Match special Teleport "managed" tag.
		// If failed to get tags for some users, log the errors instead of failing the function.
		userTags, err := f.getUserTags(ctx, user, client)
		if err != nil {
			if trace.IsAccessDenied(err) {
				f.cfg.Log.WithError(err).Debugf("No Permission to get tags for user %v", aws.StringValue(user.ARN))
			} else {
				f.cfg.Log.WithError(err).Warnf("Failed to get tags for user %v", aws.StringValue(user.ARN))
			}
			continue
		}
		for _, tag := range userTags {
			if aws.StringValue(tag.Key) == libaws.TagKeyTeleportManaged &&
				libaws.IsTagValueTrue(aws.StringValue(tag.Value)) {
				managedUsers = append(managedUsers, user)
				break
			}
		}
	}
	return managedUsers, nil
}

// getUsersForRegion discovers all ElastiCache users for provided region.
func (f *elastiCacheFetcher) getUsersForRegion(ctx context.Context, region string, client elasticacheiface.ElastiCacheAPI) ([]*elasticache.User, error) {
	getFunc := func(ctx context.Context) ([]*elasticache.User, error) {
		var users []*elasticache.User
		err := client.DescribeUsersPagesWithContext(ctx, &elasticache.DescribeUsersInput{}, func(output *elasticache.DescribeUsersOutput, _ bool) bool {
			users = append(users, output.Users...)
			return true
		})
		if err != nil {
			return nil, trace.Wrap(libaws.ConvertRequestFailureError(err))
		}
		return users, nil
	}

	users, err := libutils.FnCacheGet(ctx, f.cache, region, getFunc)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return users, nil
}

// getUserTags discovers resource tags for provided user.
func (f *elastiCacheFetcher) getUserTags(ctx context.Context, user *elasticache.User, client elasticacheiface.ElastiCacheAPI) ([]*elasticache.Tag, error) {
	getFunc := func(ctx context.Context) ([]*elasticache.Tag, error) {
		output, err := client.ListTagsForResourceWithContext(ctx, &elasticache.ListTagsForResourceInput{
			ResourceName: user.ARN,
		})
		if err != nil {
			return nil, trace.Wrap(libaws.ConvertRequestFailureError(err))
		}
		return output.TagList, nil
	}

	userTags, err := libutils.FnCacheGet(ctx, f.cache, aws.StringValue(user.ARN), getFunc)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return userTags, nil
}

// createUser creates an ElastiCache User.
func (f *elastiCacheFetcher) createUser(ecUser *elasticache.User, client elasticacheiface.ElastiCacheAPI, secrets libsecrets.Secrets) (User, error) {
	secretKey, err := secretKeyFromAWSARN(aws.StringValue(ecUser.ARN))
	if err != nil {
		return nil, trace.Wrap(err)
	}

	user := &baseUser{
		log:              f.cfg.Log,
		secretKey:        secretKey,
		secrets:          secrets,
		secretTTL:        f.cfg.Interval,
		databaseUsername: aws.StringValue(ecUser.UserName),
		clock:            f.cfg.Clock,

		// Maximum ElastiCache User password size is 128.
		// https://docs.aws.amazon.com/AmazonElastiCache/latest/red-ug/auth.html
		maxPasswordLength: 128,
		// Both Previous and Current version of the passwords are set to be
		// used for ElastiCache User. Use the Previous version for login in
		// case the Current version is not effective yet while the change is
		// being applied to the user.
		usePreviousPasswordForLogin: true,

		cloudResource: &elastiCacheUserResource{
			user:   ecUser,
			client: client,
		},
	}
	if err := user.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}
	return user, nil
}

// elastiCacheUserResource implements cloudResource interface for an
// ElastiCache user.
type elastiCacheUserResource struct {
	user   *elasticache.User
	client elasticacheiface.ElastiCacheAPI
}

// ModifyUserPassword updates passwords of an ElastiCache user.
func (r *elastiCacheUserResource) ModifyUserPassword(ctx context.Context, oldPassword, newPassword string) error {
	passwords := []string{}
	if oldPassword != "" {
		passwords = append(passwords, oldPassword)
	}
	if newPassword != "" {
		passwords = append(passwords, newPassword)
	}

	input := &elasticache.ModifyUserInput{
		UserId:             r.user.UserId,
		Passwords:          aws.StringSlice(passwords),
		NoPasswordRequired: aws.Bool(len(passwords) == 0),
	}
	if _, err := r.client.ModifyUserWithContext(ctx, input); err != nil {
		return trace.Wrap(libaws.ConvertRequestFailureError(err))
	}
	return nil
}
