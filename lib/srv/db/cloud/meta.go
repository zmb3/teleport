/*
Copyright 2021 Gravitational, Inc.

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

package cloud

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/aws/aws-sdk-go/service/elasticache"
	"github.com/aws/aws-sdk-go/service/elasticache/elasticacheiface"
	"github.com/aws/aws-sdk-go/service/memorydb"
	"github.com/aws/aws-sdk-go/service/memorydb/memorydbiface"
	"github.com/aws/aws-sdk-go/service/rds"
	"github.com/aws/aws-sdk-go/service/rds/rdsiface"
	"github.com/aws/aws-sdk-go/service/redshift"
	"github.com/aws/aws-sdk-go/service/redshift/redshiftiface"
	"github.com/gravitational/trace"
	"github.com/sirupsen/logrus"

	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/cloud"
	"github.com/zmb3/teleport/lib/services"
	"github.com/zmb3/teleport/lib/srv/db/common"
)

// MetadataConfig is the cloud metadata service config.
type MetadataConfig struct {
	// Clients is an interface for retrieving cloud clients.
	Clients cloud.Clients
}

// Check validates the metadata service config.
func (c *MetadataConfig) Check() error {
	if c.Clients == nil {
		c.Clients = cloud.NewClients()
	}
	return nil
}

// Metadata is a service that fetches cloud databases metadata.
type Metadata struct {
	cfg MetadataConfig
	log logrus.FieldLogger
}

// NewMetadata returns a new cloud metadata service.
func NewMetadata(config MetadataConfig) (*Metadata, error) {
	if err := config.Check(); err != nil {
		return nil, trace.Wrap(err)
	}
	return &Metadata{
		cfg: config,
		log: logrus.WithField(trace.Component, "meta"),
	}, nil
}

// Update updates cloud metadata of the provided database.
func (m *Metadata) Update(ctx context.Context, database types.Database) error {
	switch database.GetType() {
	case types.DatabaseTypeRDS:
		return m.updateAWS(ctx, database, m.fetchRDSMetadata)
	case types.DatabaseTypeRDSProxy:
		return m.updateAWS(ctx, database, m.fetchRDSProxyMetadata)
	case types.DatabaseTypeRedshift:
		return m.updateAWS(ctx, database, m.fetchRedshiftMetadata)
	case types.DatabaseTypeElastiCache:
		return m.updateAWS(ctx, database, m.fetchElastiCacheMetadata)
	case types.DatabaseTypeMemoryDB:
		return m.updateAWS(ctx, database, m.fetchMemoryDBMetadata)
	}
	return nil
}

// updateAWS updates cloud metadata of the provided AWS database.
func (m *Metadata) updateAWS(ctx context.Context, database types.Database, fetchFn func(context.Context, types.Database) (*types.AWS, error)) error {
	metadata, err := fetchFn(ctx, database)
	if err != nil {
		if trace.IsAccessDenied(err) { // Permission errors are expected.
			m.log.WithError(err).Debugf("No permissions to fetch metadata for %q.", database)
			return nil
		}
		return trace.Wrap(err)
	}

	m.log.Debugf("Fetched metadata for %q: %v.", database, metadata)
	database.SetStatusAWS(*metadata)
	return nil
}

// fetchRDSMetadata fetches metadata for the provided RDS or Aurora database.
func (m *Metadata) fetchRDSMetadata(ctx context.Context, database types.Database) (*types.AWS, error) {
	rds, err := m.cfg.Clients.GetAWSRDSClient(database.GetAWS().Region)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if database.GetAWS().RDS.ClusterID != "" {
		return fetchRDSClusterMetadata(ctx, rds, database.GetAWS().RDS.ClusterID)
	}

	// Try to fetch the RDS instance metadata.
	metadata, err := fetchRDSInstanceMetadata(ctx, rds, database.GetAWS().RDS.InstanceID)
	if err != nil && !trace.IsNotFound(err) && !trace.IsAccessDenied(err) {
		return nil, trace.Wrap(err)
	}
	// If RDS instance metadata wasn't found, it may be an Aurora cluster.
	if metadata == nil {
		// Aurora cluster ID may be either explicitly specified or parsed
		// from endpoint in which case it will be in InstanceID field.
		clusterID := database.GetAWS().RDS.ClusterID
		if clusterID == "" {
			clusterID = database.GetAWS().RDS.InstanceID
		}
		return fetchRDSClusterMetadata(ctx, rds, clusterID)
	}
	// If instance was found, it may be a part of an Aurora cluster.
	if metadata.RDS.ClusterID != "" {
		return fetchRDSClusterMetadata(ctx, rds, metadata.RDS.ClusterID)
	}
	return metadata, nil
}

// fetchRDSProxyMetadata fetches metadata for the provided RDS Proxy database.
func (m *Metadata) fetchRDSProxyMetadata(ctx context.Context, database types.Database) (*types.AWS, error) {
	rds, err := m.cfg.Clients.GetAWSRDSClient(database.GetAWS().Region)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if database.GetAWS().RDSProxy.CustomEndpointName != "" {
		return fetchRDSProxyCustomEndpointMetadata(ctx, rds, database.GetAWS().RDSProxy.CustomEndpointName, database.GetURI())
	}
	return fetchRDSProxyMetadata(ctx, rds, database.GetAWS().RDSProxy.Name)
}

// fetchRedshiftMetadata fetches metadata for the provided Redshift database.
func (m *Metadata) fetchRedshiftMetadata(ctx context.Context, database types.Database) (*types.AWS, error) {
	redshift, err := m.cfg.Clients.GetAWSRedshiftClient(database.GetAWS().Region)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	cluster, err := describeRedshiftCluster(ctx, redshift, database.GetAWS().Redshift.ClusterID)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	parsedARN, err := arn.Parse(aws.StringValue(cluster.ClusterNamespaceArn))
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return &types.AWS{
		Region:    parsedARN.Region,
		AccountID: parsedARN.AccountID,
		Redshift: types.Redshift{
			ClusterID: aws.StringValue(cluster.ClusterIdentifier),
		},
	}, nil
}

// fetchElastiCacheMetadata fetches metadata for the provided ElastiCache database.
func (m *Metadata) fetchElastiCacheMetadata(ctx context.Context, database types.Database) (*types.AWS, error) {
	elastiCacheClient, err := m.cfg.Clients.GetAWSElastiCacheClient(database.GetAWS().Region)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	cluster, err := describeElastiCacheCluster(ctx, elastiCacheClient, database.GetAWS().ElastiCache.ReplicationGroupID)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Endpoint type does not change.
	endpointType := database.GetAWS().ElastiCache.EndpointType
	return services.MetadataFromElastiCacheCluster(cluster, endpointType)
}

// fetchMemoryDBMetadata fetches metadata for the provided MemoryDB database.
func (m *Metadata) fetchMemoryDBMetadata(ctx context.Context, database types.Database) (*types.AWS, error) {
	memoryDBClient, err := m.cfg.Clients.GetAWSMemoryDBClient(database.GetAWS().Region)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	cluster, err := describeMemoryDBCluster(ctx, memoryDBClient, database.GetAWS().MemoryDB.ClusterName)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Endpoint type does not change.
	endpointType := database.GetAWS().MemoryDB.EndpointType
	return services.MetadataFromMemoryDBCluster(cluster, endpointType)
}

// fetchRDSInstanceMetadata fetches metadata about specified RDS instance.
func fetchRDSInstanceMetadata(ctx context.Context, rdsClient rdsiface.RDSAPI, instanceID string) (*types.AWS, error) {
	rdsInstance, err := describeRDSInstance(ctx, rdsClient, instanceID)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return services.MetadataFromRDSInstance(rdsInstance)
}

// describeRDSInstance returns AWS RDS instance for the specified ID.
func describeRDSInstance(ctx context.Context, rdsClient rdsiface.RDSAPI, instanceID string) (*rds.DBInstance, error) {
	out, err := rdsClient.DescribeDBInstancesWithContext(ctx, &rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(instanceID),
	})
	if err != nil {
		return nil, common.ConvertError(err)
	}
	if len(out.DBInstances) != 1 {
		return nil, trace.BadParameter("expected 1 RDS instance for %v, got %+v", instanceID, out.DBInstances)
	}
	return out.DBInstances[0], nil
}

// fetchRDSClusterMetadata fetches metadata about specified Aurora cluster.
func fetchRDSClusterMetadata(ctx context.Context, rdsClient rdsiface.RDSAPI, clusterID string) (*types.AWS, error) {
	rdsCluster, err := describeRDSCluster(ctx, rdsClient, clusterID)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return services.MetadataFromRDSCluster(rdsCluster)
}

// describeRDSCluster returns AWS Aurora cluster for the specified ID.
func describeRDSCluster(ctx context.Context, rdsClient rdsiface.RDSAPI, clusterID string) (*rds.DBCluster, error) {
	out, err := rdsClient.DescribeDBClustersWithContext(ctx, &rds.DescribeDBClustersInput{
		DBClusterIdentifier: aws.String(clusterID),
	})
	if err != nil {
		return nil, common.ConvertError(err)
	}
	if len(out.DBClusters) != 1 {
		return nil, trace.BadParameter("expected 1 RDS cluster for %v, got %+v", clusterID, out.DBClusters)
	}
	return out.DBClusters[0], nil
}

// describeRedshiftCluster returns AWS Redshift cluster for the specified ID.
func describeRedshiftCluster(ctx context.Context, redshiftClient redshiftiface.RedshiftAPI, clusterID string) (*redshift.Cluster, error) {
	out, err := redshiftClient.DescribeClustersWithContext(ctx, &redshift.DescribeClustersInput{
		ClusterIdentifier: aws.String(clusterID),
	})
	if err != nil {
		return nil, common.ConvertError(err)
	}
	if len(out.Clusters) != 1 {
		return nil, trace.BadParameter("expected 1 Redshift cluster for %v, got %+v", clusterID, out.Clusters)
	}
	return out.Clusters[0], nil
}

// describeElastiCacheCluster returns AWS ElastiCache Redis cluster for the
// specified ID.
func describeElastiCacheCluster(ctx context.Context, elastiCacheClient elasticacheiface.ElastiCacheAPI, replicationGroupID string) (*elasticache.ReplicationGroup, error) {
	out, err := elastiCacheClient.DescribeReplicationGroupsWithContext(ctx, &elasticache.DescribeReplicationGroupsInput{
		ReplicationGroupId: aws.String(replicationGroupID),
	})
	if err != nil {
		return nil, common.ConvertError(err)
	}
	if len(out.ReplicationGroups) != 1 {
		return nil, trace.BadParameter("expected 1 ElastiCache cluster for %v, got %+v", replicationGroupID, out.ReplicationGroups)
	}
	return out.ReplicationGroups[0], nil
}

// describeMemoryDBCluster returns AWS MemoryDB cluster for the specified ID.
func describeMemoryDBCluster(ctx context.Context, client memorydbiface.MemoryDBAPI, clusterName string) (*memorydb.Cluster, error) {
	out, err := client.DescribeClustersWithContext(ctx, &memorydb.DescribeClustersInput{
		ClusterName: aws.String(clusterName),
	})
	if err != nil {
		return nil, common.ConvertError(err)
	}
	if len(out.Clusters) != 1 {
		return nil, trace.BadParameter("expected 1 MemoryDB cluster for %v, got %+v", clusterName, out.Clusters)
	}
	return out.Clusters[0], nil
}

// fetchRDSProxyMetadata fetches metadata about specified RDS Proxy name.
func fetchRDSProxyMetadata(ctx context.Context, rdsClient rdsiface.RDSAPI, proxyName string) (*types.AWS, error) {
	rdsProxy, err := describeRDSProxy(ctx, rdsClient, proxyName)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return services.MetadataFromRDSProxy(rdsProxy)
}

// describeRDSProxy returns AWS RDS Proxy for the specified RDS Proxy name.
func describeRDSProxy(ctx context.Context, rdsClient rdsiface.RDSAPI, proxyName string) (*rds.DBProxy, error) {
	out, err := rdsClient.DescribeDBProxiesWithContext(ctx, &rds.DescribeDBProxiesInput{
		DBProxyName: aws.String(proxyName),
	})
	if err != nil {
		return nil, common.ConvertError(err)
	}
	if len(out.DBProxies) != 1 {
		return nil, trace.BadParameter("expected 1 RDS Proxy for %v, got %s", proxyName, out.DBProxies)
	}
	return out.DBProxies[0], nil
}

// fetchRDSProxyCustomEndpointMetadata fetches metadata about specified RDS
// proxy custom endpoint.
func fetchRDSProxyCustomEndpointMetadata(ctx context.Context, rdsClient rdsiface.RDSAPI, proxyEndpointName, uri string) (*types.AWS, error) {
	rdsProxyEndpoint, err := describeRDSProxyCustomEndpoint(ctx, rdsClient, proxyEndpointName, uri)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	rdsProxy, err := describeRDSProxy(ctx, rdsClient, aws.StringValue(rdsProxyEndpoint.DBProxyName))
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return services.MetadataFromRDSProxyCustomEndpoint(rdsProxy, rdsProxyEndpoint)
}

// describeRDSProxyCustomEndpoint returns AWS RDS Proxy endpoint for the
// specified RDS Proxy custom endpoint.
func describeRDSProxyCustomEndpoint(ctx context.Context, rdsClient rdsiface.RDSAPI, proxyEndpointName, uri string) (*rds.DBProxyEndpoint, error) {
	out, err := rdsClient.DescribeDBProxyEndpointsWithContext(ctx, &rds.DescribeDBProxyEndpointsInput{
		DBProxyEndpointName: aws.String(proxyEndpointName),
	})
	if err != nil {
		return nil, common.ConvertError(err)
	}
	for _, customEndpoint := range out.DBProxyEndpoints {
		// Double check if it has the same URI in case multiple custom
		// endpoints have the same name.
		if strings.Contains(uri, aws.StringValue(customEndpoint.Endpoint)) {
			return customEndpoint, nil
		}
	}
	return nil, trace.BadParameter("could not find RDS Proxy custom endpoint %v with URI %v, got %s", proxyEndpointName, uri, out.DBProxyEndpoints)
}
