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

	"github.com/google/go-cmp/cmp"
	"github.com/gravitational/trace"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/gravitational/teleport/api/types"
	resourcesv2 "github.com/gravitational/teleport/operator/apis/resources/v2"
)

var tokenSpec = &types.ProvisionTokenSpecV2{
	Roles: []types.SystemRole{types.RoleNode},
	Allow: []*types.TokenRule{
		{
			AWSAccount: "333333333333",
			AWSARN:     "arn:aws:sts::333333333333:assumed-role/teleport-node-role/i-*",
		},
	},
	JoinMethod: types.JoinMethodIAM,
}

// newProvisionTokenFromSpecNoExpire returns a new provision token with the given spec without expiration set.
func newProvisionTokenFromSpecNoExpire(token string, spec types.ProvisionTokenSpecV2) (types.ProvisionToken, error) {
	t := &types.ProvisionTokenV2{
		Metadata: types.Metadata{
			Name: token,
		},
		Spec: spec,
	}
	if err := t.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}
	return t, nil
}

type tokenTestingPrimitives struct {
	setup *testSetup
}

func (g *tokenTestingPrimitives) init(setup *testSetup) {
	g.setup = setup
}

func (g *tokenTestingPrimitives) setupTeleportFixtures(ctx context.Context) error {
	err := teleportCreateDummyRole(ctx, "testRoleA", g.setup.tClient)
	if err != nil {
		return trace.Wrap(err)
	}
	return trace.Wrap(teleportCreateDummyRole(ctx, "testRoleB", g.setup.tClient))
}

func (g *tokenTestingPrimitives) createTeleportResource(ctx context.Context, name string) error {
	token, err := newProvisionTokenFromSpecNoExpire(name, *tokenSpec)
	if err != nil {
		return trace.Wrap(err)
	}
	token.SetOrigin(types.OriginKubernetes)
	return trace.Wrap(g.setup.tClient.UpsertToken(ctx, token))
}

func (g *tokenTestingPrimitives) getTeleportResource(ctx context.Context, name string) (types.ProvisionToken, error) {
	return g.setup.tClient.GetToken(ctx, name)
}

func (g *tokenTestingPrimitives) deleteTeleportResource(ctx context.Context, name string) error {
	return trace.Wrap(g.setup.tClient.DeleteToken(ctx, name))
}

func (g *tokenTestingPrimitives) createKubernetesResource(ctx context.Context, name string) error {
	token := &resourcesv2.TeleportProvisionToken{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: g.setup.namespace.Name,
		},
		Spec: resourcesv2.TeleportProvisionTokenSpec(*tokenSpec),
	}
	return trace.Wrap(g.setup.k8sClient.Create(ctx, token))
}

func (g *tokenTestingPrimitives) deleteKubernetesResource(ctx context.Context, name string) error {
	saml := &resourcesv2.TeleportProvisionToken{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: g.setup.namespace.Name,
		},
	}
	return g.setup.k8sClient.Delete(ctx, saml)
}

func (g *tokenTestingPrimitives) getKubernetesResource(ctx context.Context, name string) (*resourcesv2.TeleportProvisionToken, error) {
	saml := &resourcesv2.TeleportProvisionToken{}
	obj := kclient.ObjectKey{
		Name:      name,
		Namespace: g.setup.namespace.Name,
	}
	err := g.setup.k8sClient.Get(ctx, obj, saml)
	return saml, trace.Wrap(err)
}

func (g *tokenTestingPrimitives) modifyKubernetesResource(ctx context.Context, name string) error {
	saml, err := g.getKubernetesResource(ctx, name)
	if err != nil {
		return trace.Wrap(err)
	}
	saml.Spec.Roles = []types.SystemRole{types.RoleNode, types.RoleProxy}
	return trace.Wrap(g.setup.k8sClient.Update(ctx, saml))
}

func (g *tokenTestingPrimitives) compareTeleportAndKubernetesResource(tResource types.ProvisionToken, kubeResource *resourcesv2.TeleportProvisionToken) (bool, string) {
	teleportMap, _ := teleportResourceToMap(tResource)
	kubernetesMap, _ := teleportResourceToMap(kubeResource.ToTeleport())

	equal := cmp.Equal(teleportMap["spec"], kubernetesMap["spec"])
	if !equal {
		return equal, cmp.Diff(teleportMap["spec"], kubernetesMap["spec"])
	}

	return equal, ""
}

func TestProvisionTokenCreation(t *testing.T) {
	test := &tokenTestingPrimitives{}
	testResourceCreation[types.ProvisionToken, *resourcesv2.TeleportProvisionToken](t, test)
}

func TestProvisionTokenDeletionDrift(t *testing.T) {
	test := &tokenTestingPrimitives{}
	testResourceDeletionDrift[types.ProvisionToken, *resourcesv2.TeleportProvisionToken](t, test)
}

func TestProvisionTokenUpdate(t *testing.T) {
	test := &tokenTestingPrimitives{}
	testResourceUpdate[types.ProvisionToken, *resourcesv2.TeleportProvisionToken](t, test)
}
