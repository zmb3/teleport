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

package iam

import (
	"fmt"

	"github.com/gravitational/trace"

	"github.com/zmb3/teleport/api/types"
	awsutils "github.com/zmb3/teleport/api/utils/aws"
	awslib "github.com/zmb3/teleport/lib/cloud/aws"
)

// GetAWSPolicyDocument returns the AWS IAM policy document for provided
// database.
func GetAWSPolicyDocument(db types.Database) (*awslib.PolicyDocument, Placeholders, error) {
	switch db.GetType() {
	case types.DatabaseTypeRDS, types.DatabaseTypeRDSProxy:
		return getRDSPolicyDocument(db)
	case types.DatabaseTypeRedshift:
		return getRedshiftPolicyDocument(db)
	default:
		return nil, nil, trace.BadParameter("GetAWSPolicyDocument is not supported policy for database type %s", db.GetType())
	}
}

// GetReadableAWSPolicyDocument returns the indented JSON string of the AWS IAM
// policy document for provided database.
func GetReadableAWSPolicyDocument(db types.Database) (string, error) {
	policyDoc, _, err := GetAWSPolicyDocument(db)
	if err != nil {
		return "", trace.Wrap(err)
	}
	marshaled, err := policyDoc.Marshal()
	if err != nil {
		return "", trace.Wrap(err)
	}
	return marshaled, nil
}

func getRDSPolicyDocument(db types.Database) (*awslib.PolicyDocument, Placeholders, error) {
	aws := db.GetAWS()
	partition := awsutils.GetPartitionFromRegion(aws.Region)
	region := aws.Region
	accountID := aws.AccountID
	resourceID := getRDSResourceID(db)

	placeholders := Placeholders(nil).
		setPlaceholderIfEmpty(&region, "{region}").
		setPlaceholderIfEmpty(&partition, "{partition}").
		setPlaceholderIfEmpty(&accountID, "{account_id}").
		setPlaceholderIfEmpty(&resourceID, "{resource_id}")

	policyDoc := awslib.NewPolicyDocument(&awslib.Statement{
		Effect:  awslib.EffectAllow,
		Actions: awslib.SliceOrString{"rds-db:connect"},
		Resources: awslib.SliceOrString{
			fmt.Sprintf("arn:%v:rds-db:%v:%v:dbuser:%v/*", partition, region, accountID, resourceID),
		},
	})
	return policyDoc, placeholders, nil
}

// getRDSResourceID returns the resource ID for RDS or RDS Proxy database.
func getRDSResourceID(db types.Database) string {
	switch db.GetType() {
	case types.DatabaseTypeRDS:
		return db.GetAWS().RDS.ResourceID
	case types.DatabaseTypeRDSProxy:
		return db.GetAWS().RDSProxy.ResourceID
	default:
		return ""
	}
}

func getRedshiftPolicyDocument(db types.Database) (*awslib.PolicyDocument, Placeholders, error) {
	aws := db.GetAWS()
	partition := awsutils.GetPartitionFromRegion(aws.Region)
	region := aws.Region
	accountID := aws.AccountID
	clusterID := aws.Redshift.ClusterID

	placeholders := Placeholders(nil).
		setPlaceholderIfEmpty(&region, "{region}").
		setPlaceholderIfEmpty(&partition, "{partition}").
		setPlaceholderIfEmpty(&accountID, "{account_id}").
		setPlaceholderIfEmpty(&clusterID, "{cluster_id}")

	policyDoc := awslib.NewPolicyDocument(&awslib.Statement{
		Effect:  awslib.EffectAllow,
		Actions: awslib.SliceOrString{"redshift:GetClusterCredentials"},
		Resources: awslib.SliceOrString{
			fmt.Sprintf("arn:%v:redshift:%v:%v:dbuser:%v/*", partition, region, accountID, clusterID),
			fmt.Sprintf("arn:%v:redshift:%v:%v:dbname:%v/*", partition, region, accountID, clusterID),
			fmt.Sprintf("arn:%v:redshift:%v:%v:dbgroup:%v/*", partition, region, accountID, clusterID),
		},
	})
	return policyDoc, placeholders, nil
}

// Placeholders defines a slice of strings used as placeholders.
type Placeholders []string

func (s Placeholders) setPlaceholderIfEmpty(attr *string, placeholder string) Placeholders {
	if attr == nil || *attr != "" {
		return s
	}
	*attr = placeholder
	return append(s, placeholder)
}
