// Copyright 2022 Gravitational, Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package aws

import (
	"context"
	"sort"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	awssession "github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ssm"
	"github.com/aws/aws-sdk-go/service/ssm/ssmiface"
	"github.com/aws/aws-sdk-go/service/sts"
	"github.com/aws/aws-sdk-go/service/sts/stsiface"
	"github.com/google/go-cmp/cmp"
	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/api/constants"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/api/utils"
	awslib "github.com/zmb3/teleport/lib/cloud/aws"
	"github.com/zmb3/teleport/lib/config"
	"github.com/zmb3/teleport/lib/configurators"
	"github.com/zmb3/teleport/lib/services"
)

func TestAWSIAMDocuments(t *testing.T) {
	userTarget, err := awslib.IdentityFromArn("arn:aws:iam::1234567:user/example-user")
	require.NoError(t, err)

	roleTarget, err := awslib.IdentityFromArn("arn:aws:iam::1234567:role/example-role")
	require.NoError(t, err)

	unknownIdentity, err := awslib.IdentityFromArn("arn:aws:iam::1234567:ec2/example-ec2")
	require.NoError(t, err)

	tests := map[string]struct {
		returnError        bool
		flags              configurators.BootstrapFlags
		fileConfig         *config.FileConfig
		target             awslib.Identity
		statements         []*awslib.Statement
		boundaryStatements []*awslib.Statement
	}{
		"RDSAutoDiscoveryToUser": {
			target: userTarget,
			fileConfig: &config.FileConfig{
				Databases: config.Databases{
					AWSMatchers: []config.AWSMatcher{
						{Types: []string{services.AWSMatcherRDS}, Regions: []string{"us-west-2"}},
					},
				},
			},
			statements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"rds:DescribeDBInstances", "rds:ModifyDBInstance",
					"rds:DescribeDBClusters", "rds:ModifyDBCluster",
				}},
				{Effect: awslib.EffectAllow, Resources: []string{userTarget.String()}, Actions: []string{
					"iam:GetUserPolicy", "iam:PutUserPolicy", "iam:DeleteUserPolicy",
				}},
			},
			boundaryStatements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"rds:DescribeDBInstances", "rds:ModifyDBInstance",
					"rds:DescribeDBClusters", "rds:ModifyDBCluster",
					"rds-db:connect",
				}},
				{Effect: awslib.EffectAllow, Resources: []string{userTarget.String()}, Actions: []string{
					"iam:GetUserPolicy", "iam:PutUserPolicy", "iam:DeleteUserPolicy",
				}},
			},
		},
		"RDSAutoDiscoveryToRole": {
			target: roleTarget,
			fileConfig: &config.FileConfig{
				Databases: config.Databases{
					AWSMatchers: []config.AWSMatcher{
						{Types: []string{services.AWSMatcherRDS}, Regions: []string{"us-west-2"}},
					},
				},
			},
			statements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"rds:DescribeDBInstances", "rds:ModifyDBInstance",
					"rds:DescribeDBClusters", "rds:ModifyDBCluster",
				}},
				{Effect: awslib.EffectAllow, Resources: []string{roleTarget.String()}, Actions: []string{
					"iam:GetRolePolicy", "iam:PutRolePolicy", "iam:DeleteRolePolicy",
				}},
			},
			boundaryStatements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"rds:DescribeDBInstances", "rds:ModifyDBInstance",
					"rds:DescribeDBClusters", "rds:ModifyDBCluster",
					"rds-db:connect",
				}},
				{Effect: awslib.EffectAllow, Resources: []string{roleTarget.String()}, Actions: []string{
					"iam:GetRolePolicy", "iam:PutRolePolicy", "iam:DeleteRolePolicy",
				}},
			},
		},
		"RDS static database": {
			target: roleTarget,
			fileConfig: &config.FileConfig{
				Databases: config.Databases{
					Databases: []*config.Database{
						{
							Name:     "aurora-1",
							Protocol: "postgres",
							URI:      "aurora-instance-1.abcdefghijklmnop.us-west-1.rds.amazonaws.com:5432",
						},
					},
				},
			},
			statements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"rds:DescribeDBInstances", "rds:ModifyDBInstance",
					"rds:DescribeDBClusters", "rds:ModifyDBCluster",
				}},
				{Effect: awslib.EffectAllow, Resources: []string{roleTarget.String()}, Actions: []string{
					"iam:GetRolePolicy", "iam:PutRolePolicy", "iam:DeleteRolePolicy",
				}},
			},
			boundaryStatements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"rds:DescribeDBInstances", "rds:ModifyDBInstance",
					"rds:DescribeDBClusters", "rds:ModifyDBCluster",
					"rds-db:connect",
				}},
				{Effect: awslib.EffectAllow, Resources: []string{roleTarget.String()}, Actions: []string{
					"iam:GetRolePolicy", "iam:PutRolePolicy", "iam:DeleteRolePolicy",
				}},
			},
		},
		"RedshiftAutoDiscoveryToUser": {
			target: userTarget,
			fileConfig: &config.FileConfig{
				Databases: config.Databases{
					AWSMatchers: []config.AWSMatcher{
						{Types: []string{services.AWSMatcherRedshift}, Regions: []string{"us-west-2"}},
					},
				},
			},
			statements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"redshift:DescribeClusters",
				}},
				{Effect: awslib.EffectAllow, Resources: []string{userTarget.String()}, Actions: []string{
					"iam:GetUserPolicy", "iam:PutUserPolicy", "iam:DeleteUserPolicy",
				}},
			},
			boundaryStatements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"redshift:DescribeClusters", "redshift:GetClusterCredentials",
				}},
				{Effect: awslib.EffectAllow, Resources: []string{userTarget.String()}, Actions: []string{
					"iam:GetUserPolicy", "iam:PutUserPolicy", "iam:DeleteUserPolicy",
				}},
			},
		},
		"RedshiftAutoDiscoveryToRole": {
			target: roleTarget,
			fileConfig: &config.FileConfig{
				Databases: config.Databases{
					AWSMatchers: []config.AWSMatcher{
						{Types: []string{services.AWSMatcherRedshift}, Regions: []string{"us-west-2"}},
					},
				},
			},
			statements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"redshift:DescribeClusters",
				}},
				{Effect: awslib.EffectAllow, Resources: []string{roleTarget.String()}, Actions: []string{
					"iam:GetRolePolicy", "iam:PutRolePolicy", "iam:DeleteRolePolicy",
				}},
			},
			boundaryStatements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"redshift:DescribeClusters", "redshift:GetClusterCredentials",
				}},
				{Effect: awslib.EffectAllow, Resources: []string{roleTarget.String()}, Actions: []string{
					"iam:GetRolePolicy", "iam:PutRolePolicy", "iam:DeleteRolePolicy",
				}},
			},
		},
		"RedshiftDatabases": {
			target: roleTarget,
			fileConfig: &config.FileConfig{
				Databases: config.Databases{
					Databases: []*config.Database{
						{
							Name: "redshift-cluster-1",
							URI:  "redshift-cluster-1.abcdefghijkl.us-west-2.redshift.amazonaws.com:5439",
						},
					},
				},
			},
			statements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"redshift:DescribeClusters",
				}},
				{Effect: awslib.EffectAllow, Resources: []string{roleTarget.String()}, Actions: []string{
					"iam:GetRolePolicy", "iam:PutRolePolicy", "iam:DeleteRolePolicy",
				}},
			},
			boundaryStatements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"redshift:DescribeClusters", "redshift:GetClusterCredentials",
				}},
				{Effect: awslib.EffectAllow, Resources: []string{roleTarget.String()}, Actions: []string{
					"iam:GetRolePolicy", "iam:PutRolePolicy", "iam:DeleteRolePolicy",
				}},
			},
		},
		"ElastiCache auto discovery": {
			target: roleTarget,
			fileConfig: &config.FileConfig{
				Databases: config.Databases{
					AWSMatchers: []config.AWSMatcher{
						{Types: []string{services.AWSMatcherElastiCache}, Regions: []string{"us-west-2"}},
					},
				},
			},
			statements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"elasticache:ListTagsForResource",
					"elasticache:DescribeReplicationGroups",
					"elasticache:DescribeCacheClusters",
					"elasticache:DescribeCacheSubnetGroups",
					"elasticache:DescribeUsers",
					"elasticache:ModifyUser",
				}},
				{
					Effect: awslib.EffectAllow,
					Actions: []string{
						"secretsmanager:DescribeSecret", "secretsmanager:CreateSecret",
						"secretsmanager:UpdateSecret", "secretsmanager:DeleteSecret",
						"secretsmanager:GetSecretValue", "secretsmanager:PutSecretValue",
						"secretsmanager:TagResource",
					},
					Resources: []string{"arn:aws:secretsmanager:*:1234567:secret:teleport/*"},
				},
			},
			boundaryStatements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"elasticache:ListTagsForResource",
					"elasticache:DescribeReplicationGroups",
					"elasticache:DescribeCacheClusters",
					"elasticache:DescribeCacheSubnetGroups",
					"elasticache:DescribeUsers",
					"elasticache:ModifyUser",
				}},
				{
					Effect: awslib.EffectAllow,
					Actions: []string{
						"secretsmanager:DescribeSecret", "secretsmanager:CreateSecret",
						"secretsmanager:UpdateSecret", "secretsmanager:DeleteSecret",
						"secretsmanager:GetSecretValue", "secretsmanager:PutSecretValue",
						"secretsmanager:TagResource",
					},
					Resources: []string{"arn:aws:secretsmanager:*:1234567:secret:teleport/*"},
				},
			},
		},
		"ElastiCache static database": {
			target: roleTarget,
			fileConfig: &config.FileConfig{
				Databases: config.Databases{
					Databases: []*config.Database{
						{
							Name: "redis-1",
							URI:  "clustercfg.redis1.xxxxxx.usw2.cache.amazonaws.com:6379",
						},
						{
							Name: "redis-2",
							URI:  "clustercfg.redis2.xxxxxx.usw2.cache.amazonaws.com:6379",
							AWS: config.DatabaseAWS{
								SecretStore: config.SecretStore{
									KeyPrefix: "my-prefix/",
									KMSKeyID:  "my-kms-id",
								},
							},
						},
					},
				},
			},
			statements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"elasticache:ListTagsForResource",
					"elasticache:DescribeReplicationGroups",
					"elasticache:DescribeCacheClusters",
					"elasticache:DescribeCacheSubnetGroups",
					"elasticache:DescribeUsers",
					"elasticache:ModifyUser",
				}},
				{
					Effect: "Allow",
					Actions: []string{
						"secretsmanager:DescribeSecret", "secretsmanager:CreateSecret",
						"secretsmanager:UpdateSecret", "secretsmanager:DeleteSecret",
						"secretsmanager:GetSecretValue", "secretsmanager:PutSecretValue",
						"secretsmanager:TagResource",
					},
					Resources: []string{
						"arn:aws:secretsmanager:*:1234567:secret:teleport/*",
						"arn:aws:secretsmanager:*:1234567:secret:my-prefix/*",
					},
				},
				{
					Effect:  "Allow",
					Actions: []string{"kms:GenerateDataKey", "kms:Decrypt"},
					Resources: []string{
						"arn:aws:kms:*:1234567:key/my-kms-id",
					},
				},
			},
			boundaryStatements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"elasticache:ListTagsForResource",
					"elasticache:DescribeReplicationGroups",
					"elasticache:DescribeCacheClusters",
					"elasticache:DescribeCacheSubnetGroups",
					"elasticache:DescribeUsers",
					"elasticache:ModifyUser",
				}},
				{
					Effect: "Allow",
					Actions: []string{
						"secretsmanager:DescribeSecret", "secretsmanager:CreateSecret",
						"secretsmanager:UpdateSecret", "secretsmanager:DeleteSecret",
						"secretsmanager:GetSecretValue", "secretsmanager:PutSecretValue",
						"secretsmanager:TagResource",
					},
					Resources: []string{
						"arn:aws:secretsmanager:*:1234567:secret:teleport/*",
						"arn:aws:secretsmanager:*:1234567:secret:my-prefix/*",
					},
				},
				{
					Effect:  "Allow",
					Actions: []string{"kms:GenerateDataKey", "kms:Decrypt"},
					Resources: []string{
						"arn:aws:kms:*:1234567:key/my-kms-id",
					},
				},
			},
		},
		"MemoryDB auto discovery": {
			target: roleTarget,
			fileConfig: &config.FileConfig{
				Databases: config.Databases{
					AWSMatchers: []config.AWSMatcher{
						{Types: []string{services.AWSMatcherMemoryDB}, Regions: []string{"us-west-2"}},
					},
				},
			},
			statements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"memorydb:ListTags",
					"memorydb:DescribeClusters",
					"memorydb:DescribeSubnetGroups",
					"memorydb:DescribeUsers",
					"memorydb:UpdateUser",
				}},
				{
					Effect: awslib.EffectAllow,
					Actions: []string{
						"secretsmanager:DescribeSecret", "secretsmanager:CreateSecret",
						"secretsmanager:UpdateSecret", "secretsmanager:DeleteSecret",
						"secretsmanager:GetSecretValue", "secretsmanager:PutSecretValue",
						"secretsmanager:TagResource",
					},
					Resources: []string{"arn:aws:secretsmanager:*:1234567:secret:teleport/*"},
				},
			},
			boundaryStatements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"memorydb:ListTags",
					"memorydb:DescribeClusters",
					"memorydb:DescribeSubnetGroups",
					"memorydb:DescribeUsers",
					"memorydb:UpdateUser",
				}},
				{
					Effect: awslib.EffectAllow,
					Actions: []string{
						"secretsmanager:DescribeSecret", "secretsmanager:CreateSecret",
						"secretsmanager:UpdateSecret", "secretsmanager:DeleteSecret",
						"secretsmanager:GetSecretValue", "secretsmanager:PutSecretValue",
						"secretsmanager:TagResource",
					},
					Resources: []string{"arn:aws:secretsmanager:*:1234567:secret:teleport/*"},
				},
			},
		},
		"MemoryDB static database": {
			target: roleTarget,
			fileConfig: &config.FileConfig{
				Databases: config.Databases{
					Databases: []*config.Database{
						{
							Name: "memorydb-1",
							URI:  "clustercfg.memorydb1.xxxxxx.us-east-1.memorydb.amazonaws.com:6379",
						},
						{
							Name: "memorydb-2",
							URI:  "clustercfg.memorydb0.xxxxxx.us-east-1.memorydb.amazonaws.com:6379",
							AWS: config.DatabaseAWS{
								SecretStore: config.SecretStore{
									KeyPrefix: "my-prefix/",
									KMSKeyID:  "my-kms-id",
								},
							},
						},
					},
				},
			},
			statements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"memorydb:ListTags",
					"memorydb:DescribeClusters",
					"memorydb:DescribeSubnetGroups",
					"memorydb:DescribeUsers",
					"memorydb:UpdateUser",
				}},
				{
					Effect: "Allow",
					Actions: []string{
						"secretsmanager:DescribeSecret", "secretsmanager:CreateSecret",
						"secretsmanager:UpdateSecret", "secretsmanager:DeleteSecret",
						"secretsmanager:GetSecretValue", "secretsmanager:PutSecretValue",
						"secretsmanager:TagResource",
					},
					Resources: []string{
						"arn:aws:secretsmanager:*:1234567:secret:teleport/*",
						"arn:aws:secretsmanager:*:1234567:secret:my-prefix/*",
					},
				},
				{
					Effect:  "Allow",
					Actions: []string{"kms:GenerateDataKey", "kms:Decrypt"},
					Resources: []string{
						"arn:aws:kms:*:1234567:key/my-kms-id",
					},
				},
			},
			boundaryStatements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"memorydb:ListTags",
					"memorydb:DescribeClusters",
					"memorydb:DescribeSubnetGroups",
					"memorydb:DescribeUsers",
					"memorydb:UpdateUser",
				}},
				{
					Effect: "Allow",
					Actions: []string{
						"secretsmanager:DescribeSecret", "secretsmanager:CreateSecret",
						"secretsmanager:UpdateSecret", "secretsmanager:DeleteSecret",
						"secretsmanager:GetSecretValue", "secretsmanager:PutSecretValue",
						"secretsmanager:TagResource",
					},
					Resources: []string{
						"arn:aws:secretsmanager:*:1234567:secret:teleport/*",
						"arn:aws:secretsmanager:*:1234567:secret:my-prefix/*",
					},
				},
				{
					Effect:  "Allow",
					Actions: []string{"kms:GenerateDataKey", "kms:Decrypt"},
					Resources: []string{
						"arn:aws:kms:*:1234567:key/my-kms-id",
					},
				},
			},
		},
		"AutoDiscovery EC2": {
			target: roleTarget,
			flags:  configurators.BootstrapFlags{DiscoveryService: true},
			fileConfig: &config.FileConfig{
				Discovery: config.Discovery{
					AWSMatchers: []config.AWSMatcher{
						{
							Types:   []string{constants.AWSServiceTypeEC2},
							Regions: []string{"eu-central-1"},
							Tags:    map[string]utils.Strings{"*": []string{"*"}},
							InstallParams: &config.InstallParams{
								JoinParams: config.JoinParams{
									TokenName: "token",
									Method:    types.JoinMethodEC2,
								},
							},
						},
					},
				},
			},

			statements: []*awslib.Statement{
				{
					Effect: "Allow",
					Actions: []string{
						"ec2:DescribeInstances",
						"ssm:GetCommandInvocation",
						"ssm:SendCommand"},
					Resources: []string{"*"},
				},
			},
			boundaryStatements: []*awslib.Statement{
				{
					Effect: "Allow",
					Actions: []string{
						"ec2:DescribeInstances",
						"ssm:GetCommandInvocation",
						"ssm:SendCommand"},
					Resources: []string{"*"},
				},
			},
		},
		"AutoDiscoveryUnknownIdentity": {
			returnError: true,
			target:      unknownIdentity,
			fileConfig: &config.FileConfig{
				Databases: config.Databases{
					AWSMatchers: []config.AWSMatcher{
						{Types: []string{services.AWSMatcherRDS}, Regions: []string{"us-west-2"}},
					},
				},
			},
		},
		"RDS Proxy discovery": {
			target: userTarget,
			fileConfig: &config.FileConfig{
				Databases: config.Databases{
					AWSMatchers: []config.AWSMatcher{
						{Types: []string{services.AWSMatcherRDSProxy}, Regions: []string{"us-west-2"}},
					},
				},
			},
			statements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"rds:DescribeDBProxies", "rds:DescribeDBProxyEndpoints", "rds:DescribeDBProxyTargets", "rds:ListTagsForResource",
				}},
				{Effect: awslib.EffectAllow, Resources: []string{userTarget.String()}, Actions: []string{
					"iam:GetUserPolicy", "iam:PutUserPolicy", "iam:DeleteUserPolicy",
				}},
			},
			boundaryStatements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"rds:DescribeDBProxies", "rds:DescribeDBProxyEndpoints", "rds:DescribeDBProxyTargets", "rds:ListTagsForResource",
					"rds-db:connect",
				}},
				{Effect: awslib.EffectAllow, Resources: []string{userTarget.String()}, Actions: []string{
					"iam:GetUserPolicy", "iam:PutUserPolicy", "iam:DeleteUserPolicy",
				}},
			},
		},
		"RDS Proxy static database": {
			target: userTarget,
			fileConfig: &config.FileConfig{
				Databases: config.Databases{
					Databases: []*config.Database{
						{
							Name:     "rds-proxy-1",
							Protocol: "postgres",
							URI:      "my-proxy.proxy-abcdefghijklmnop.us-west-1.rds.amazonaws.com:5432",
						},
					},
				},
			},
			statements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"rds:DescribeDBProxies", "rds:DescribeDBProxyEndpoints", "rds:DescribeDBProxyTargets", "rds:ListTagsForResource",
				}},
				{Effect: awslib.EffectAllow, Resources: []string{userTarget.String()}, Actions: []string{
					"iam:GetUserPolicy", "iam:PutUserPolicy", "iam:DeleteUserPolicy",
				}},
			},
			boundaryStatements: []*awslib.Statement{
				{Effect: awslib.EffectAllow, Resources: []string{"*"}, Actions: []string{
					"rds:DescribeDBProxies", "rds:DescribeDBProxyEndpoints", "rds:DescribeDBProxyTargets", "rds:ListTagsForResource",
					"rds-db:connect",
				}},
				{Effect: awslib.EffectAllow, Resources: []string{userTarget.String()}, Actions: []string{
					"iam:GetUserPolicy", "iam:PutUserPolicy", "iam:DeleteUserPolicy",
				}},
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			policy, policyErr := buildPolicyDocument(test.flags, test.fileConfig, test.target)
			boundary, boundaryErr := buildPolicyBoundaryDocument(test.flags, test.fileConfig, test.target)

			if test.returnError {
				require.Error(t, policyErr)
				require.Error(t, boundaryErr)
				return
			}

			sortStringsTrans := cmp.Transformer("SortStrings", func(in []string) []string {
				out := append([]string(nil), in...) // Copy input to avoid mutating it
				sort.Strings(out)
				return out
			})
			require.Empty(t, cmp.Diff(test.statements, policy.Document.Statements, sortStringsTrans))
			require.Empty(t, cmp.Diff(test.boundaryStatements, boundary.Document.Statements, sortStringsTrans))
		})
	}
}

func TestAWSPolicyCreator(t *testing.T) {
	ctx := context.Background()

	tests := map[string]struct {
		returnError bool
		isBoundary  bool
		policies    *policiesMock
	}{
		"UpsertPolicy": {
			policies: &policiesMock{
				upsertArn: "generated-arn",
			},
		},
		"UpsertPolicyBoundary": {
			isBoundary: true,
			policies: &policiesMock{
				upsertArn: "generated-arn",
			},
		},
		"UpsertPolicyFailure": {
			returnError: true,
			policies: &policiesMock{
				upsertError: trace.NotImplemented("upsert not implemented"),
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			action := &awsPolicyCreator{
				policies:   test.policies,
				isBoundary: test.isBoundary,
			}

			actionCtx := &configurators.ConfiguratorActionContext{}
			err := action.Execute(ctx, actionCtx)
			if test.returnError {
				require.Error(t, err)
				return
			}

			if test.isBoundary {
				require.Equal(t, test.policies.upsertArn, actionCtx.AWSPolicyBoundaryArn)
				return
			}

			require.Equal(t, test.policies.upsertArn, actionCtx.AWSPolicyArn)
		})
	}
}

func TestAWSPoliciesAttacher(t *testing.T) {
	ctx := context.Background()
	userTarget, err := awslib.IdentityFromArn("arn:aws:iam::1234567:user/example-user")
	require.NoError(t, err)

	roleTarget, err := awslib.IdentityFromArn("arn:aws:iam::1234567:role/example-role")
	require.NoError(t, err)

	tests := map[string]struct {
		returnError bool
		target      awslib.Identity
		policies    *policiesMock
		actionCtx   *configurators.ConfiguratorActionContext
	}{
		"AttachPoliciesToUser": {
			target:   userTarget,
			policies: &policiesMock{},
			actionCtx: &configurators.ConfiguratorActionContext{
				AWSPolicyArn:         "policy-arn",
				AWSPolicyBoundaryArn: "policy-boundary-arn",
			},
		},
		"AttachPoliciesToRole": {
			target:   roleTarget,
			policies: &policiesMock{},
			actionCtx: &configurators.ConfiguratorActionContext{
				AWSPolicyArn:         "policy-arn",
				AWSPolicyBoundaryArn: "policy-boundary-arn",
			},
		},
		"MissingPolicyArn": {
			returnError: true,
			target:      roleTarget,
			policies:    &policiesMock{},
			actionCtx: &configurators.ConfiguratorActionContext{
				AWSPolicyBoundaryArn: "policy-boundary-arn",
			},
		},
		"MissingPolicyBoundaryArn": {
			returnError: true,
			target:      roleTarget,
			policies:    &policiesMock{},
			actionCtx: &configurators.ConfiguratorActionContext{
				AWSPolicyArn: "policy-arn",
			},
		},
		"AttachPolicyFailure": {
			returnError: true,
			target:      roleTarget,
			policies: &policiesMock{
				attachError: trace.NotImplemented("attach policy not implemented"),
			},
			actionCtx: &configurators.ConfiguratorActionContext{
				AWSPolicyArn:         "policy-arn",
				AWSPolicyBoundaryArn: "policy-boundary-arn",
			},
		},
		"AttachPolicyBoundaryFailure": {
			returnError: true,
			target:      roleTarget,
			policies: &policiesMock{
				attachBoundaryError: trace.NotImplemented("attach policy not implemented"),
			},
			actionCtx: &configurators.ConfiguratorActionContext{
				AWSPolicyArn:         "policy-arn",
				AWSPolicyBoundaryArn: "policy-boundary-arn",
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			action := &awsPoliciesAttacher{policies: test.policies, target: test.target}
			err := action.Execute(ctx, test.actionCtx)
			if test.returnError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestAWSPoliciesTarget(t *testing.T) {
	userIdentity, err := awslib.IdentityFromArn("arn:aws:iam::1234567:user/example-user")
	require.NoError(t, err)

	roleIdentity, err := awslib.IdentityFromArn("arn:aws:iam::1234567:role/example-role")
	require.NoError(t, err)

	tests := map[string]struct {
		flags             configurators.BootstrapFlags
		identity          awslib.Identity
		accountID         string
		partitionID       string
		targetType        awslib.Identity
		targetName        string
		targetAccountID   string
		targetPartitionID string
	}{
		"UserNameFromFlags": {
			flags:             configurators.BootstrapFlags{AttachToUser: "example-user"},
			accountID:         "123456",
			partitionID:       "aws",
			targetType:        awslib.User{},
			targetName:        "example-user",
			targetAccountID:   "123456",
			targetPartitionID: "aws",
		},
		"UserARNFromFlags": {
			flags:             configurators.BootstrapFlags{AttachToUser: "arn:aws:iam::123456:user/example-user"},
			targetType:        awslib.User{},
			targetName:        "example-user",
			targetAccountID:   "123456",
			targetPartitionID: "aws",
		},
		"RoleNameFromFlags": {
			flags:             configurators.BootstrapFlags{AttachToRole: "example-role"},
			accountID:         "123456",
			partitionID:       "aws",
			targetType:        awslib.Role{},
			targetName:        "example-role",
			targetAccountID:   "123456",
			targetPartitionID: "aws",
		},
		"RoleARNFromFlags": {
			flags:             configurators.BootstrapFlags{AttachToRole: "arn:aws:iam::123456:role/example-role"},
			targetType:        awslib.Role{},
			targetName:        "example-role",
			targetAccountID:   "123456",
			targetPartitionID: "aws",
		},
		"UserFromIdentity": {
			flags:             configurators.BootstrapFlags{},
			identity:          userIdentity,
			targetType:        awslib.User{},
			targetName:        userIdentity.GetName(),
			targetAccountID:   userIdentity.GetAccountID(),
			targetPartitionID: userIdentity.GetPartition(),
		},
		"RoleFromIdentity": {
			flags:             configurators.BootstrapFlags{},
			identity:          roleIdentity,
			targetType:        awslib.Role{},
			targetName:        roleIdentity.GetName(),
			targetAccountID:   roleIdentity.GetAccountID(),
			targetPartitionID: roleIdentity.GetPartition(),
		},
		"DefaultTarget": {
			flags:             configurators.BootstrapFlags{},
			accountID:         "*",
			partitionID:       "*",
			targetType:        awslib.User{},
			targetName:        defaultAttachUser,
			targetAccountID:   "*",
			targetPartitionID: "*",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			target, err := policiesTarget(test.flags, test.accountID, test.partitionID, test.identity)
			require.NoError(t, err)
			require.IsType(t, test.targetType, target)
			require.Equal(t, test.targetName, target.GetName())
			require.Equal(t, test.targetAccountID, target.GetAccountID())
			require.Equal(t, test.targetPartitionID, target.GetPartition())
		})
	}
}

func TestAWSDocumentConfigurator(t *testing.T) {
	var err error
	ctx := context.Background()

	config := ConfiguratorConfig{
		AWSSession:   &awssession.Session{},
		AWSSTSClient: &STSMock{ARN: "arn:aws:iam::1234567:role/example-role"},
		AWSSSMClient: &SSMMock{
			t: t,
			expectedInput: &ssm.CreateDocumentInput{
				Content:        aws.String(EC2DiscoverySSMDocument("https://proxy.example.org:443")),
				DocumentType:   aws.String("Command"),
				DocumentFormat: aws.String("YAML"),
				Name:           aws.String("document"),
			},
		},
		FileConfig: &config.FileConfig{
			Proxy: config.Proxy{
				PublicAddr: []string{"proxy.example.org:443"},
			},
			Discovery: config.Discovery{
				AWSMatchers: []config.AWSMatcher{
					{
						Types:   []string{"ec2"},
						Regions: []string{"eu-central-1"},
						SSM:     config.AWSSSM{DocumentName: "document"},
					},
				},
			},
		},
		Flags: configurators.BootstrapFlags{
			DiscoveryService:    true,
			ForceEC2Permissions: true,
		},
		Policies: &policiesMock{
			upsertArn: "polcies-arn",
		},
	}
	configurator, err := NewAWSConfigurator(config)
	require.NoError(t, err)
	require.False(t, configurator.IsEmpty())

	// Execute actions.
	actionCtx := &configurators.ConfiguratorActionContext{}
	for _, action := range configurator.Actions() {
		err = action.Execute(ctx, actionCtx)
		require.NoError(t, err)
	}

}

// TestAWSConfigurator tests all actions together.
func TestAWSConfigurator(t *testing.T) {
	var err error
	ctx := context.Background()

	config := ConfiguratorConfig{
		AWSSession:   &awssession.Session{},
		AWSSTSClient: &STSMock{ARN: "arn:aws:iam::1234567:role/example-role"},
		AWSSSMClient: &SSMMock{},
		FileConfig:   &config.FileConfig{},
		Flags: configurators.BootstrapFlags{
			AttachToUser:        "some-user",
			ForceRDSPermissions: true,
		},
		Policies: &policiesMock{
			upsertArn: "polcies-arn",
		},
	}

	configurator, err := NewAWSConfigurator(config)
	require.NoError(t, err)
	require.False(t, configurator.IsEmpty())

	// Execute actions.
	actionCtx := &configurators.ConfiguratorActionContext{}
	for _, action := range configurator.Actions() {
		err = action.Execute(ctx, actionCtx)
		require.NoError(t, err)
	}

	config.Flags.DiscoveryService = true
	config.Flags.ForceEC2Permissions = true

	configurator, err = NewAWSConfigurator(config)
	require.NoError(t, err)
	require.False(t, configurator.IsEmpty())

	// Execute actions.
	actionCtx = &configurators.ConfiguratorActionContext{}
	for _, action := range configurator.Actions() {
		err = action.Execute(ctx, actionCtx)
		require.NoError(t, err)
	}

}

type policiesMock struct {
	awslib.Policies

	upsertArn           string
	upsertError         error
	attachError         error
	attachBoundaryError error
}

func (p *policiesMock) Upsert(context.Context, *awslib.Policy) (string, error) {
	return p.upsertArn, p.upsertError
}

func (p *policiesMock) Attach(context.Context, string, awslib.Identity) error {
	return p.attachError
}

func (p *policiesMock) AttachBoundary(context.Context, string, awslib.Identity) error {
	return p.attachBoundaryError
}

type STSMock struct {
	stsiface.STSAPI
	ARN               string
	callerIdentityErr error
}

func (m *STSMock) GetCallerIdentityWithContext(aws.Context, *sts.GetCallerIdentityInput, ...request.Option) (*sts.GetCallerIdentityOutput, error) {
	return &sts.GetCallerIdentityOutput{
		Arn: aws.String(m.ARN),
	}, m.callerIdentityErr
}

type SSMMock struct {
	ssmiface.SSMAPI

	t             *testing.T
	expectedInput *ssm.CreateDocumentInput
}

func (m *SSMMock) CreateDocumentWithContext(ctx aws.Context, input *ssm.CreateDocumentInput, opts ...request.Option) (*ssm.CreateDocumentOutput, error) {

	m.t.Helper()
	require.Equal(m.t, m.expectedInput, input)

	return nil, nil
}
