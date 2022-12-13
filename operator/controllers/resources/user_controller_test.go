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

package resources

import (
	"context"
	"testing"

	"github.com/gravitational/trace"
	"github.com/mitchellh/mapstructure"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/util/retry"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/zmb3/teleport/api/types"
	resourcesv2 "github.com/zmb3/teleport/operator/apis/resources/v2"
	resourcesv5 "github.com/zmb3/teleport/operator/apis/resources/v5"
)

const teleportUserKind = "TeleportUser"

var teleportUserGVK = schema.GroupVersionKind{
	Group:   resourcesv2.GroupVersion.Group,
	Version: resourcesv2.GroupVersion.Version,
	Kind:    teleportUserKind,
}

func TestUserCreation(t *testing.T) {
	ctx := context.Background()
	setup := setupTestEnv(t)
	userName := validRandomResourceName("user-")

	require.NoError(t, teleportCreateDummyRole(ctx, "a", setup.tClient))
	require.NoError(t, teleportCreateDummyRole(ctx, "b", setup.tClient))

	// The user is created in K8S
	k8sCreateDummyUser(ctx, t, setup.k8sClient, setup.namespace.Name, userName)

	fastEventually(t, func() bool {
		tUser, err := setup.tClient.GetUser(userName, false)
		if trace.IsNotFound(err) {
			return false
		}
		require.NoError(t, err)

		require.Equal(t, tUser.GetName(), userName)

		require.Contains(t, tUser.GetMetadata().Labels, types.OriginLabel)
		require.Equal(t, tUser.GetMetadata().Labels[types.OriginLabel], types.OriginKubernetes)

		return true
	})

	// The user is deleted in K8S
	k8sDeleteUser(ctx, t, setup.k8sClient, userName, setup.namespace.Name)

	fastEventually(t, func() bool {
		_, err := setup.tClient.GetUser(userName, false)
		return trace.IsNotFound(err)
	})
}

func TestUserCreationFromYAML(t *testing.T) {
	ctx := context.Background()
	setup := setupTestEnv(t)
	require.NoError(t, teleportCreateDummyRole(ctx, "a", setup.tClient))
	tests := []struct {
		name         string
		userSpecYAML string
		shouldFail   bool
		expectedSpec *types.UserSpecV2
	}{
		{
			name: "Valid user without traits",
			userSpecYAML: `
roles:
  - a
`,
			shouldFail: false,
			expectedSpec: &types.UserSpecV2{
				Roles: []string{"a"},
			},
		},
		{
			name: "Valid user with trait (list with single element)",
			userSpecYAML: `
roles:
  - a
traits:
  'foo': ['bar']
`,
			shouldFail: false,
			expectedSpec: &types.UserSpecV2{
				Roles: []string{"a"},
				Traits: map[string][]string{
					"foo": {"bar"},
				},
			},
		},
		{
			name: "Valid user with traits (list with multiple element)",
			userSpecYAML: `
roles:
  - a
traits:
  'foo': ['bar', 'baz']
`,
			shouldFail: false,
			expectedSpec: &types.UserSpecV2{
				Roles: []string{"a"},
				Traits: map[string][]string{
					"foo": {"bar", "baz"},
				},
			},
		},
		{
			name: "Invalid user with non-existing role",
			userSpecYAML: `
roles:
  - does-not-exist
traits:
  'foo': ['bar', 'baz']
`,
			shouldFail:   true,
			expectedSpec: nil,
		},
	}

	for _, tc := range tests {
		tc := tc // capture range variable
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Creating the Kubernetes resource. We are using an untyped client to be able to create invalid resources.
			userManifest := map[string]interface{}{}
			err := yaml.Unmarshal([]byte(tc.userSpecYAML), &userManifest)
			require.NoError(t, err)

			userName := validRandomResourceName("user-")

			obj := getUnstructuredObjectFromGVK(teleportUserGVK)
			obj.Object["spec"] = userManifest
			obj.SetName(userName)
			obj.SetNamespace(setup.namespace.Name)
			err = setup.k8sClient.Create(ctx, obj)
			require.NoError(t, err)

			// If failure is expected we should not see the resource in Teleport
			if tc.shouldFail {
				fastEventually(t, func() bool {
					// We check status.Conditions was updated, this means the reconciliation happened
					_ = setup.k8sClient.Get(ctx, kclient.ObjectKey{
						Namespace: setup.namespace.Name,
						Name:      userName,
					}, obj)
					errorConditions := getUserStatusConditionError(obj.Object)
					// If there's no error condition, reconciliation has not happened yet
					if len(errorConditions) == 0 {
						return false
					}

					_, err := setup.tClient.GetUser(userName, false /* withSecrets */)
					require.True(t, trace.IsNotFound(err), "The user should not be created in Teleport")
					return true
				})
			} else {
				// We wait for Teleport resource creation
				fastEventually(t, func() bool {
					tUser, err := setup.tClient.GetUser(userName, false /* withSecrets */)
					// If the resource creation should succeed we check the resource was found and validate ownership labels
					if trace.IsNotFound(err) {
						return false
					}
					require.NoError(t, err)

					require.Equal(t, tUser.GetName(), userName)
					require.Contains(t, tUser.GetMetadata().Labels, types.OriginLabel)
					require.Equal(t, tUser.GetMetadata().Labels[types.OriginLabel], types.OriginKubernetes)
					require.Equal(t, setup.operatorName, tUser.GetCreatedBy().User.Name)
					expectedUser := &types.UserV2{
						Metadata: types.Metadata{},
						Spec:     *tc.expectedSpec,
					}
					_ = expectedUser.CheckAndSetDefaults()
					compareUserSpecs(t, expectedUser, tUser)

					return true
				})
			}
			// Teardown

			// The role is deleted in K8S
			k8sDeleteUser(ctx, t, setup.k8sClient, userName, setup.namespace.Name)

			// We wait for the role deletion in Teleport
			fastEventually(t, func() bool {
				_, err := setup.tClient.GetUser(userName, false /* withSecrets */)
				return trace.IsNotFound(err)
			})
		})
	}
}

func compareUserSpecs(t *testing.T, expectedUser, actualUser types.User) {
	expected, err := teleportResourceToMap(expectedUser)
	require.NoError(t, err)
	actual, err := teleportResourceToMap(actualUser)
	require.NoError(t, err)

	// We don't want compare spec.created_by and metadata as they were tested before and are not 100%
	// managed by the operator
	delete(expected["spec"].(map[string]interface{}), "created_by")
	delete(actual["spec"].(map[string]interface{}), "created_by")

	require.Equal(t, expected["spec"], actual["spec"])
}

// TestUserDeletionDrift tests how the Kubernetes operator reacts when it is asked to delete a user that was
// already deleted in Teleport
func TestUserDeletionDrift(t *testing.T) {
	// Setup section: start the operator, and create a user
	ctx := context.Background()
	setup := setupTestEnv(t)
	userName := validRandomResourceName("user-")

	require.NoError(t, teleportCreateDummyRole(ctx, "a", setup.tClient))
	require.NoError(t, teleportCreateDummyRole(ctx, "b", setup.tClient))

	// The user is created in K8S
	k8sCreateDummyUser(ctx, t, setup.k8sClient, setup.namespace.Name, userName)

	fastEventually(t, func() bool {
		tUser, err := setup.tClient.GetUser(userName, false)
		if trace.IsNotFound(err) {
			return false
		}
		require.NoError(t, err)

		require.Equal(t, tUser.GetName(), userName)

		require.Contains(t, tUser.GetMetadata().Labels, types.OriginLabel)
		require.Equal(t, tUser.GetMetadata().Labels[types.OriginLabel], types.OriginKubernetes)

		return true
	})
	// We cause a drift by altering the Teleport resource.
	// To make sure the operator does not reconcile while we're finished we suspend the operator
	setup.stopKubernetesOperator()

	err := setup.tClient.DeleteUser(ctx, userName)
	require.NoError(t, err)
	fastEventually(t, func() bool {
		_, err := setup.tClient.GetUser(userName, false)
		return trace.IsNotFound(err)
	})

	// We flag the role for deletion in Kubernetes (it won't be fully remopved until the operator has processed it and removed the finalizer)
	k8sDeleteUser(ctx, t, setup.k8sClient, userName, setup.namespace.Name)

	// Test section: We resume the operator, it should reconcile and recover from the drift
	setup.startKubernetesOperator(t)

	// The operator should handle the failed Teleport deletion gracefully and unlock the Kubernetes resource deletion
	var k8sUser resourcesv2.TeleportUser
	fastEventually(t, func() bool {
		err = setup.k8sClient.Get(ctx, kclient.ObjectKey{
			Namespace: setup.namespace.Name,
			Name:      userName,
		}, &k8sUser)
		return kerrors.IsNotFound(err)
	})
}

func TestUserUpdate(t *testing.T) {
	ctx := context.Background()
	setup := setupTestEnv(t)
	require.NoError(t, teleportCreateDummyRole(ctx, "a", setup.tClient))
	require.NoError(t, teleportCreateDummyRole(ctx, "b", setup.tClient))
	require.NoError(t, teleportCreateDummyRole(ctx, "x", setup.tClient))
	require.NoError(t, teleportCreateDummyRole(ctx, "y", setup.tClient))
	require.NoError(t, teleportCreateDummyRole(ctx, "z", setup.tClient))

	userName := validRandomResourceName("user-")

	// The user does not exist in K8S
	var r resourcesv2.TeleportUser
	err := setup.k8sClient.Get(ctx, kclient.ObjectKey{
		Namespace: setup.namespace.Name,
		Name:      userName,
	}, &r)
	require.True(t, kerrors.IsNotFound(err))

	// The user is created in Teleport
	tUser, err := types.NewUser(userName)
	require.NoError(t, err)
	tUser.SetRoles([]string{"a", "b"})
	metadata := tUser.GetMetadata()
	metadata.Labels = map[string]string{types.OriginLabel: types.OriginKubernetes}
	tUser.SetMetadata(metadata)

	err = setup.tClient.CreateUser(ctx, tUser)
	require.NoError(t, err)

	// The user is created in K8S
	k8sUser := resourcesv2.TeleportUser{
		ObjectMeta: metav1.ObjectMeta{
			Name:      userName,
			Namespace: setup.namespace.Name,
		},
		Spec: resourcesv2.TeleportUserSpec{
			Roles: []string{"x", "z"},
		},
	}
	k8sCreateUser(ctx, t, setup.k8sClient, &k8sUser)

	// The user is updated in Teleport
	fastEventually(t, func() bool {
		tUser, err := setup.tClient.GetUser(userName, false)
		require.NoError(t, err)

		// TeleportUser was updated with new roles
		return assert.ElementsMatch(t, tUser.GetRoles(), []string{"x", "z"})
	})

	// Updating the user in K8S
	// The modification can fail because of a conflict with the resource controller. We retry if that happens.
	var k8sUserNewVersion resourcesv2.TeleportUser
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		err := setup.k8sClient.Get(ctx, kclient.ObjectKey{
			Namespace: setup.namespace.Name,
			Name:      userName,
		}, &k8sUserNewVersion)
		if err != nil {
			return err
		}

		k8sUserNewVersion.Spec.Roles = append(k8sUserNewVersion.Spec.Roles, "y")
		return setup.k8sClient.Update(ctx, &k8sUserNewVersion)
	})
	require.NoError(t, err)

	// Updates the user in Teleport
	fastEventually(t, func() bool {
		tUser, err := setup.tClient.GetUser(userName, false)
		require.NoError(t, err)

		// TeleportUser updated with new roles
		return assert.ElementsMatch(t, tUser.GetRoles(), []string{"x", "z", "y"})
	})
}

func k8sCreateDummyUser(ctx context.Context, t *testing.T, kc kclient.Client, namespace, userName string) {
	user := resourcesv2.TeleportUser{
		ObjectMeta: metav1.ObjectMeta{
			Name:      userName,
			Namespace: namespace,
		},
		Spec: resourcesv2.TeleportUserSpec{
			Roles: []string{"a", "b"},
		},
	}
	k8sCreateUser(ctx, t, kc, &user)
}

func k8sDeleteUser(ctx context.Context, t *testing.T, kc kclient.Client, userName, namespace string) {
	user := resourcesv2.TeleportUser{
		ObjectMeta: metav1.ObjectMeta{
			Name:      userName,
			Namespace: namespace,
		},
	}
	err := kc.Delete(ctx, &user)
	require.NoError(t, err)
}

func k8sCreateUser(ctx context.Context, t *testing.T, kc kclient.Client, user *resourcesv2.TeleportUser) {
	err := kc.Create(ctx, user)
	require.NoError(t, err)
}

func TestAddTeleportResourceOriginUser(t *testing.T) {
	r := UserReconciler{}
	tests := []struct {
		name     string
		resource types.User
	}{
		{
			name: "origin already set correctly",
			resource: &types.UserV2{
				Metadata: types.Metadata{
					Name:   "user with correct origin",
					Labels: map[string]string{types.OriginLabel: types.OriginKubernetes},
				},
			},
		},
		{
			name: "origin already set incorrectly",
			resource: &types.UserV2{
				Metadata: types.Metadata{
					Name:   "user with correct origin",
					Labels: map[string]string{types.OriginLabel: types.OriginConfigFile},
				},
			},
		},
		{
			name: "origin not set",
			resource: &types.UserV2{
				Metadata: types.Metadata{
					Name:   "user with correct origin",
					Labels: map[string]string{"foo": "bar"},
				},
			},
		},
		{
			name: "no labels",
			resource: &types.UserV2{
				Metadata: types.Metadata{
					Name: "user with no labels",
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r.addTeleportResourceOrigin(tc.resource)
			metadata := tc.resource.GetMetadata()
			require.Contains(t, metadata.Labels, types.OriginLabel)
			require.Equal(t, metadata.Labels[types.OriginLabel], types.OriginKubernetes)
		})
	}
}

func getUserStatusConditionError(object map[string]interface{}) []metav1.Condition {
	var conditionsWithError []metav1.Condition
	var status resourcesv5.TeleportRoleStatus
	_ = mapstructure.Decode(object["status"], &status)

	for _, condition := range status.Conditions {
		if condition.Status == metav1.ConditionFalse {
			conditionsWithError = append(conditionsWithError, condition)
		}
	}
	return conditionsWithError
}
