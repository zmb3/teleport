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

package services

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v2"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redisenterprise/armredisenterprise"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/sql/armsql"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/aws/aws-sdk-go/service/elasticache"
	"github.com/aws/aws-sdk-go/service/memorydb"
	"github.com/aws/aws-sdk-go/service/rds"
	"github.com/aws/aws-sdk-go/service/redshift"
	"github.com/aws/aws-sdk-go/service/redshiftserverless"
	"github.com/coreos/go-semver/semver"
	"github.com/gravitational/trace"
	log "github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/x/mongo/driver/connstring"
	"golang.org/x/exp/slices"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/gravitational/teleport/api/types"
	awsutils "github.com/gravitational/teleport/api/utils/aws"
	azureutils "github.com/gravitational/teleport/api/utils/azure"
	libcloudaws "github.com/gravitational/teleport/lib/cloud/aws"
	"github.com/gravitational/teleport/lib/cloud/azure"
	"github.com/gravitational/teleport/lib/defaults"
	"github.com/gravitational/teleport/lib/srv/db/redis/connection"
	"github.com/gravitational/teleport/lib/tlsca"
	"github.com/gravitational/teleport/lib/utils"
)

// DatabaseGetter defines interface for fetching database resources.
type DatabaseGetter interface {
	// GetDatabases returns all database resources.
	GetDatabases(context.Context) ([]types.Database, error)
	// GetDatabase returns the specified database resource.
	GetDatabase(ctx context.Context, name string) (types.Database, error)
}

// Databases defines an interface for managing database resources.
type Databases interface {
	// DatabaseGetter provides methods for fetching database resources.
	DatabaseGetter
	// CreateDatabase creates a new database resource.
	CreateDatabase(context.Context, types.Database) error
	// UpdateDatabase updates an existing database resource.
	UpdateDatabase(context.Context, types.Database) error
	// DeleteDatabase removes the specified database resource.
	DeleteDatabase(ctx context.Context, name string) error
	// DeleteAllDatabases removes all database resources.
	DeleteAllDatabases(context.Context) error
}

// MarshalDatabase marshals the database resource to JSON.
func MarshalDatabase(database types.Database, opts ...MarshalOption) ([]byte, error) {
	if err := database.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}
	cfg, err := CollectOptions(opts)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	switch database := database.(type) {
	case *types.DatabaseV3:
		if !cfg.PreserveResourceID {
			// avoid modifying the original object
			// to prevent unexpected data races
			copy := *database
			copy.SetResourceID(0)
			database = &copy
		}
		return utils.FastMarshal(database)
	default:
		return nil, trace.BadParameter("unsupported database resource %T", database)
	}
}

// UnmarshalDatabase unmarshals the database resource from JSON.
func UnmarshalDatabase(data []byte, opts ...MarshalOption) (types.Database, error) {
	if len(data) == 0 {
		return nil, trace.BadParameter("missing database resource data")
	}
	cfg, err := CollectOptions(opts)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	var h types.ResourceHeader
	if err := utils.FastUnmarshal(data, &h); err != nil {
		return nil, trace.Wrap(err)
	}
	switch h.Version {
	case types.V3:
		var database types.DatabaseV3
		if err := utils.FastUnmarshal(data, &database); err != nil {
			return nil, trace.BadParameter(err.Error())
		}
		if err := database.CheckAndSetDefaults(); err != nil {
			return nil, trace.Wrap(err)
		}
		if cfg.ID != 0 {
			database.SetResourceID(cfg.ID)
		}
		if !cfg.Expires.IsZero() {
			database.SetExpiry(cfg.Expires)
		}
		return &database, nil
	}
	return nil, trace.BadParameter("unsupported database resource version %q", h.Version)
}

// ValidateDatabase validates a types.Database.
func ValidateDatabase(db types.Database) error {
	if err := db.CheckAndSetDefaults(); err != nil {
		return trace.Wrap(err)
	}

	// Unlike application access proxy, database proxy name doesn't necessarily
	// need to be a valid subdomain but use the same validation logic for the
	// simplicity and consistency.
	if errs := validation.IsDNS1035Label(db.GetName()); len(errs) > 0 {
		return trace.BadParameter("invalid database %q name: %v", db.GetName(), errs)
	}

	if !slices.Contains(defaults.DatabaseProtocols, db.GetProtocol()) {
		return trace.BadParameter("unsupported database %q protocol %q, supported are: %v", db.GetName(), db.GetProtocol(), defaults.DatabaseProtocols)
	}

	// For MongoDB we support specifying either server address or connection
	// string in the URI which is useful when connecting to a replica set.
	if db.GetProtocol() == defaults.ProtocolMongoDB &&
		(strings.HasPrefix(db.GetURI(), connstring.SchemeMongoDB+"://") ||
			strings.HasPrefix(db.GetURI(), connstring.SchemeMongoDBSRV+"://")) {
		if err := validateMongoDB(db); err != nil {
			return trace.Wrap(err)
		}
	} else if db.GetProtocol() == defaults.ProtocolRedis {
		_, err := connection.ParseRedisAddress(db.GetURI())
		if err != nil {
			return trace.BadParameter("invalid Redis database %q address: %q, error: %v", db.GetName(), db.GetURI(), err)
		}
	} else if db.GetProtocol() == defaults.ProtocolSnowflake {
		if !strings.Contains(db.GetURI(), defaults.SnowflakeURL) {
			return trace.BadParameter("Snowflake address should contain " + defaults.SnowflakeURL)
		}
	} else if db.GetProtocol() == defaults.ProtocolCassandra && db.GetAWS().Region != "" && db.GetAWS().AccountID != "" {
		// In case of cloud hosted Cassandra doesn't require URI validation.
	} else if _, _, err := net.SplitHostPort(db.GetURI()); err != nil {
		return trace.BadParameter("invalid database %q address %q: %v", db.GetName(), db.GetURI(), err)
	}

	if db.GetTLS().CACert != "" {
		if _, err := tlsca.ParseCertificatePEM([]byte(db.GetTLS().CACert)); err != nil {
			return trace.BadParameter("provided database %q CA doesn't appear to be a valid x509 certificate: %v", db.GetName(), err)
		}
	}

	// Validate Active Directory specific configuration, when Kerberos auth is required.
	if db.GetProtocol() == defaults.ProtocolSQLServer && (db.GetAD().Domain != "" || !strings.Contains(db.GetURI(), azureutils.MSSQLEndpointSuffix)) {

		if db.GetAD().LDAPCert == "" && db.GetAD().KDCHostName == "" {
			if db.GetAD().KeytabFile == "" {
				return trace.BadParameter("missing keytab file path for database %q", db.GetName())
			}
		} else {
			if db.GetAD().LDAPCert == "" {
				return trace.BadParameter("missing Windows LDAP certificate from active directory for database %q", db.GetName())
			}
			if db.GetAD().KDCHostName == "" {
				return trace.BadParameter("missing kdc_host_name for x509 SQL Server configuration for database %q", db.GetName())
			}
		}

		if db.GetAD().Krb5File == "" {
			return trace.BadParameter("missing keytab file path for database %q", db.GetName())
		}

		if db.GetAD().Domain == "" {
			return trace.BadParameter("missing Active Directory domain for database %q", db.GetName())
		}
		if db.GetAD().SPN == "" {
			return trace.BadParameter("missing service principal name for database %q", db.GetName())
		}
	}
	return nil
}

// validateMongoDB validates MongoDB URIs with "mongodb" schemes.
func validateMongoDB(db types.Database) error {
	connString, err := connstring.ParseAndValidate(db.GetURI())
	// connstring.ParseAndValidate requires DNS resolution on TXT/SRV records
	// for a full validation for "mongodb+srv" URIs. We will try to skip the
	// DNS errors here by replacing the scheme and then ParseAndValidate again
	// to validate as much as we can.
	if isDNSError(err) {
		log.Warnf("MongoDB database %q (connection string %q) failed validation with DNS error: %v.", db.GetName(), db.GetURI(), err)

		connString, err = connstring.ParseAndValidate(strings.Replace(
			db.GetURI(),
			connstring.SchemeMongoDBSRV+"://",
			connstring.SchemeMongoDB+"://",
			1,
		))
	}
	if err != nil {
		return trace.BadParameter("invalid MongoDB database %q connection string %q: %v", db.GetName(), db.GetURI(), err)
	}

	// Validate read preference to catch typos early.
	if connString.ReadPreference != "" {
		if _, err := readpref.ModeFromString(connString.ReadPreference); err != nil {
			return trace.BadParameter("invalid MongoDB database %q read preference %q", db.GetName(), connString.ReadPreference)
		}
	}
	return nil
}

func isDNSError(err error) bool {
	if err == nil {
		return false
	}

	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

// setDBNameByLabel modifies the types.Metadata argument in place, setting the database name.
// The name is calculated based on nameParts arguments which are joined by hyphens "-".
// If the DB name override label is present, it will replace the *first* name part.
func setDBNameByLabel(overrideLabel string, meta types.Metadata, firstNamePart string, extraNameParts ...string) types.Metadata {
	nameParts := append([]string{firstNamePart}, extraNameParts...)

	// apply override
	if override, found := meta.Labels[overrideLabel]; found && override != "" {
		nameParts[0] = override
	}

	meta.Name = strings.Join(nameParts, "-")

	return meta
}

// setDBName sets database name if override label labelTeleportDBName is present.
func setDBName(meta types.Metadata, firstNamePart string, extraNameParts ...string) types.Metadata {
	return setDBNameByLabel(labelTeleportDBName, meta, firstNamePart, extraNameParts...)
}

// setDBName sets database name if override label labelTeleportDBNameAzure is present.
func setAzureDBName(meta types.Metadata, firstNamePart string, extraNameParts ...string) types.Metadata {
	return setDBNameByLabel(labelTeleportDBNameAzure, meta, firstNamePart, extraNameParts...)
}

// NewDatabaseFromAzureServer creates a database resource from an AzureDB server.
func NewDatabaseFromAzureServer(server *azure.DBServer) (types.Database, error) {
	fqdn := server.Properties.FullyQualifiedDomainName
	if fqdn == "" {
		return nil, trace.BadParameter("empty FQDN")
	}
	labels, err := labelsFromAzureServer(server)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return types.NewDatabaseV3(
		setAzureDBName(types.Metadata{
			Description: fmt.Sprintf("Azure %v server in %v",
				defaults.ReadableDatabaseProtocol(server.Protocol),
				server.Location),
			Labels: labels,
		}, server.Name),
		types.DatabaseSpecV3{
			Protocol: server.Protocol,
			URI:      fmt.Sprintf("%v:%v", fqdn, server.Port),
			Azure: types.Azure{
				Name:       server.Name,
				ResourceID: server.ID,
			},
		})
}

// NewDatabaseFromAzureRedis creates a database resource from an Azure Redis server.
func NewDatabaseFromAzureRedis(server *armredis.ResourceInfo) (types.Database, error) {
	if server.Properties == nil {
		return nil, trace.BadParameter("missing properties")
	}
	if server.Properties.SSLPort == nil {
		return nil, trace.BadParameter("missing SSL port")
	}
	labels, err := labelsFromAzureRedis(server)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return types.NewDatabaseV3(
		setAzureDBName(types.Metadata{
			Description: fmt.Sprintf("Azure Redis server in %v", azure.StringVal(server.Location)),
			Labels:      labels,
		}, azure.StringVal(server.Name)),
		types.DatabaseSpecV3{
			Protocol: defaults.ProtocolRedis,
			URI:      fmt.Sprintf("%v:%v", azure.StringVal(server.Properties.HostName), *server.Properties.SSLPort),
			Azure: types.Azure{
				Name:       azure.StringVal(server.Name),
				ResourceID: azure.StringVal(server.ID),
			},
		})
}

// NewDatabaseFromAzureRedisEnterprise creates a database resource from an
// Azure Redis Enterprise database and its parent cluster.
func NewDatabaseFromAzureRedisEnterprise(cluster *armredisenterprise.Cluster, database *armredisenterprise.Database) (types.Database, error) {
	if cluster.Properties == nil || database.Properties == nil {
		return nil, trace.BadParameter("missing properties")
	}
	if database.Properties.Port == nil {
		return nil, trace.BadParameter("missing port")
	}
	labels, err := labelsFromAzureRedisEnterprise(cluster, database)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// If the database name is "default", use only the cluster name as the name.
	// If the database name is not "default", use "<cluster>-<database>" as the name.
	var nameSuffix []string
	if azure.StringVal(database.Name) != azure.RedisEnterpriseClusterDefaultDatabase {
		nameSuffix = append(nameSuffix, azure.StringVal(database.Name))
	}

	return types.NewDatabaseV3(
		setAzureDBName(types.Metadata{
			Description: fmt.Sprintf("Azure Redis Enterprise server in %v", azure.StringVal(cluster.Location)),
			Labels:      labels,
		}, azure.StringVal(cluster.Name), nameSuffix...),
		types.DatabaseSpecV3{
			Protocol: defaults.ProtocolRedis,
			URI:      fmt.Sprintf("%v:%v", azure.StringVal(cluster.Properties.HostName), *database.Properties.Port),
			Azure: types.Azure{
				ResourceID: azure.StringVal(database.ID),
				Redis: types.AzureRedis{
					ClusteringPolicy: azure.StringVal(database.Properties.ClusteringPolicy),
				},
			},
		})
}

// NewDatabaseFromAzureSQLServer creates a database resource from an Azure SQL
// server.
func NewDatabaseFromAzureSQLServer(server *armsql.Server) (types.Database, error) {
	if server.Properties == nil {
		return nil, trace.BadParameter("missing properties")
	}

	if server.Properties.FullyQualifiedDomainName == nil {
		return nil, trace.BadParameter("missing FQDN")
	}

	labels, err := labelsFromAzureSQLServer(server)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return types.NewDatabaseV3(
		setAzureDBName(types.Metadata{
			Description: fmt.Sprintf("Azure SQL Server in %v", azure.StringVal(server.Location)),
			Labels:      labels,
		}, azure.StringVal(server.Name)),
		types.DatabaseSpecV3{
			Protocol: defaults.ProtocolSQLServer,
			URI:      fmt.Sprintf("%v:%d", azure.StringVal(server.Properties.FullyQualifiedDomainName), azureSQLServerDefaultPort),
			Azure: types.Azure{
				Name:       azure.StringVal(server.Name),
				ResourceID: azure.StringVal(server.ID),
			},
		})
}

// NewDatabaseFromAzureManagedSQLServer creates a database resource from an
// Azure Managed SQL server.
func NewDatabaseFromAzureManagedSQLServer(server *armsql.ManagedInstance) (types.Database, error) {
	if server.Properties == nil {
		return nil, trace.BadParameter("missing properties")
	}

	if server.Properties.FullyQualifiedDomainName == nil {
		return nil, trace.BadParameter("missing FQDN")
	}

	labels, err := labelsFromAzureManagedSQLServer(server)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return types.NewDatabaseV3(
		setAzureDBName(types.Metadata{
			Description: fmt.Sprintf("Azure Managed SQL Server in %v", azure.StringVal(server.Location)),
			Labels:      labels,
		}, azure.StringVal(server.Name)),
		types.DatabaseSpecV3{
			Protocol: defaults.ProtocolSQLServer,
			URI:      fmt.Sprintf("%v:%d", azure.StringVal(server.Properties.FullyQualifiedDomainName), azureSQLServerDefaultPort),
			Azure: types.Azure{
				Name:       azure.StringVal(server.Name),
				ResourceID: azure.StringVal(server.ID),
			},
		})
}

// NewDatabaseFromRDSInstance creates a database resource from an RDS instance.
func NewDatabaseFromRDSInstance(instance *rds.DBInstance) (types.Database, error) {
	endpoint := instance.Endpoint
	if endpoint == nil {
		return nil, trace.BadParameter("empty endpoint")
	}
	metadata, err := MetadataFromRDSInstance(instance)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	protocol, err := rdsEngineToProtocol(aws.StringValue(instance.Engine))
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return types.NewDatabaseV3(
		setDBName(types.Metadata{
			Description: fmt.Sprintf("RDS instance in %v", metadata.Region),
			Labels:      labelsFromRDSInstance(instance, metadata),
		}, aws.StringValue(instance.DBInstanceIdentifier)),
		types.DatabaseSpecV3{
			Protocol: protocol,
			URI:      fmt.Sprintf("%v:%v", aws.StringValue(endpoint.Address), aws.Int64Value(endpoint.Port)),
			AWS:      *metadata,
		})
}

// NewDatabaseFromRDSCluster creates a database resource from an RDS cluster (Aurora).
func NewDatabaseFromRDSCluster(cluster *rds.DBCluster) (types.Database, error) {
	metadata, err := MetadataFromRDSCluster(cluster)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	protocol, err := rdsEngineToProtocol(aws.StringValue(cluster.Engine))
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return types.NewDatabaseV3(
		setDBName(types.Metadata{
			Description: fmt.Sprintf("Aurora cluster in %v", metadata.Region),
			Labels:      labelsFromRDSCluster(cluster, metadata, RDSEndpointTypePrimary),
		}, aws.StringValue(cluster.DBClusterIdentifier)),
		types.DatabaseSpecV3{
			Protocol: protocol,
			URI:      fmt.Sprintf("%v:%v", aws.StringValue(cluster.Endpoint), aws.Int64Value(cluster.Port)),
			AWS:      *metadata,
		})
}

// NewDatabaseFromRDSClusterReaderEndpoint creates a database resource from an RDS cluster reader endpoint (Aurora).
func NewDatabaseFromRDSClusterReaderEndpoint(cluster *rds.DBCluster) (types.Database, error) {
	metadata, err := MetadataFromRDSCluster(cluster)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	protocol, err := rdsEngineToProtocol(aws.StringValue(cluster.Engine))
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return types.NewDatabaseV3(
		setDBName(types.Metadata{
			Description: fmt.Sprintf("Aurora cluster in %v (%v endpoint)", metadata.Region, string(RDSEndpointTypeReader)),
			Labels:      labelsFromRDSCluster(cluster, metadata, RDSEndpointTypeReader),
		}, aws.StringValue(cluster.DBClusterIdentifier), string(RDSEndpointTypeReader)),
		types.DatabaseSpecV3{
			Protocol: protocol,
			URI:      fmt.Sprintf("%v:%v", aws.StringValue(cluster.ReaderEndpoint), aws.Int64Value(cluster.Port)),
			AWS:      *metadata,
		})
}

// NewDatabasesFromRDSClusterCustomEndpoints creates database resources from RDS cluster custom endpoints (Aurora).
func NewDatabasesFromRDSClusterCustomEndpoints(cluster *rds.DBCluster) (types.Databases, error) {
	metadata, err := MetadataFromRDSCluster(cluster)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	protocol, err := rdsEngineToProtocol(aws.StringValue(cluster.Engine))
	if err != nil {
		return nil, trace.Wrap(err)
	}

	var errors []error
	var databases types.Databases
	for _, endpoint := range cluster.CustomEndpoints {
		// RDS custom endpoint format:
		// <endpointName>.cluster-custom-<customerDnsIdentifier>.<dnsSuffix>
		endpointDetails, err := awsutils.ParseRDSEndpoint(aws.StringValue(endpoint))
		if err != nil {
			errors = append(errors, trace.Wrap(err))
			continue
		}
		if endpointDetails.ClusterCustomEndpointName == "" {
			errors = append(errors, trace.BadParameter("missing Aurora custom endpoint name"))
			continue
		}

		database, err := types.NewDatabaseV3(
			setDBName(types.Metadata{
				Description: fmt.Sprintf("Aurora cluster in %v (%v endpoint)", metadata.Region, string(RDSEndpointTypeCustom)),
				Labels:      labelsFromRDSCluster(cluster, metadata, RDSEndpointTypeCustom),
			}, aws.StringValue(cluster.DBClusterIdentifier), string(RDSEndpointTypeCustom), endpointDetails.ClusterCustomEndpointName),
			types.DatabaseSpecV3{
				Protocol: protocol,
				URI:      fmt.Sprintf("%v:%v", aws.StringValue(endpoint), aws.Int64Value(cluster.Port)),
				AWS:      *metadata,

				// Aurora instances update their certificates upon restart, and thus custom endpoint SAN may not be available right
				// away. Using primary endpoint instead as server name since it's always available.
				TLS: types.DatabaseTLS{
					ServerName: aws.StringValue(cluster.Endpoint),
				},
			})
		if err != nil {
			errors = append(errors, trace.Wrap(err))
			continue
		}

		databases = append(databases, database)
	}

	return databases, trace.NewAggregate(errors...)
}

// NewDatabaseFromRDSProxy creates database resource from RDS Proxy.
func NewDatabaseFromRDSProxy(dbProxy *rds.DBProxy, port int64, tags []*rds.Tag) (types.Database, error) {
	metadata, err := MetadataFromRDSProxy(dbProxy)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	protocol, err := rdsEngineFamilyToProtocol(aws.StringValue(dbProxy.EngineFamily))
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return types.NewDatabaseV3(
		setDBName(types.Metadata{
			Description: fmt.Sprintf("RDS Proxy in %v", metadata.Region),
			Labels:      labelsFromRDSProxy(dbProxy, metadata, tags),
		}, aws.StringValue(dbProxy.DBProxyName)),
		types.DatabaseSpecV3{
			Protocol: protocol,
			URI:      fmt.Sprintf("%s:%d", aws.StringValue(dbProxy.Endpoint), port),
			AWS:      *metadata,
		})
}

// NewDatabaseFromRDSProxyCustomEndpiont creates database resource from RDS
// Proxy custom endpoint.
func NewDatabaseFromRDSProxyCustomEndpoint(dbProxy *rds.DBProxy, customEndpoint *rds.DBProxyEndpoint, port int64, tags []*rds.Tag) (types.Database, error) {
	metadata, err := MetadataFromRDSProxyCustomEndpoint(dbProxy, customEndpoint)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	protocol, err := rdsEngineFamilyToProtocol(aws.StringValue(dbProxy.EngineFamily))
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return types.NewDatabaseV3(
		setDBName(types.Metadata{
			Description: fmt.Sprintf("RDS Proxy endpoint in %v", metadata.Region),
			Labels:      labelsFromRDSProxyCustomEndpoint(dbProxy, customEndpoint, metadata, tags),
		}, aws.StringValue(dbProxy.DBProxyName), aws.StringValue(customEndpoint.DBProxyEndpointName)),
		types.DatabaseSpecV3{
			Protocol: protocol,
			URI:      fmt.Sprintf("%s:%d", aws.StringValue(customEndpoint.Endpoint), port),
			AWS:      *metadata,

			// RDS proxies serve wildcard certificates like this:
			// *.proxy-<xxx>.<region>.rds.amazonaws.com
			//
			// However the custom endpoints have one extra level of subdomains like:
			// <name>.endpoint.proxy-<xxx>.<region>.rds.amazonaws.com
			// which will fail verify_full against the wildcard certificates.
			//
			// Using proxy's default endpoint as server name as it should always
			// succeed.
			TLS: types.DatabaseTLS{
				ServerName: aws.StringValue(dbProxy.Endpoint),
			},
		})
}

// NewDatabaseFromRedshiftCluster creates a database resource from a Redshift cluster.
func NewDatabaseFromRedshiftCluster(cluster *redshift.Cluster) (types.Database, error) {
	// Endpoint can be nil while the cluster is being created. Return an error
	// until the Endpoint is available.
	if cluster.Endpoint == nil {
		return nil, trace.BadParameter("missing endpoint in Redshift cluster %v", aws.StringValue(cluster.ClusterIdentifier))
	}

	metadata, err := MetadataFromRedshiftCluster(cluster)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return types.NewDatabaseV3(
		setDBName(types.Metadata{
			Description: fmt.Sprintf("Redshift cluster in %v", metadata.Region),
			Labels:      labelsFromRedshiftCluster(cluster, metadata),
		}, aws.StringValue(cluster.ClusterIdentifier)),
		types.DatabaseSpecV3{
			Protocol: defaults.ProtocolPostgres,
			URI:      fmt.Sprintf("%v:%v", aws.StringValue(cluster.Endpoint.Address), aws.Int64Value(cluster.Endpoint.Port)),
			AWS:      *metadata,
		})
}

// NewDatabaseFromElastiCacheConfigurationEndpoint creates a database resource
// from ElastiCache configuration endpoint.
func NewDatabaseFromElastiCacheConfigurationEndpoint(cluster *elasticache.ReplicationGroup, extraLabels map[string]string) (types.Database, error) {
	if cluster.ConfigurationEndpoint == nil {
		return nil, trace.BadParameter("missing configuration endpoint")
	}

	return newElastiCacheDatabase(cluster, cluster.ConfigurationEndpoint, awsutils.ElastiCacheConfigurationEndpoint, extraLabels)
}

// NewDatabasesFromElastiCacheNodeGroups creates database resources from
// ElastiCache node groups.
func NewDatabasesFromElastiCacheNodeGroups(cluster *elasticache.ReplicationGroup, extraLabels map[string]string) (types.Databases, error) {
	var databases types.Databases
	for _, nodeGroup := range cluster.NodeGroups {
		if nodeGroup.PrimaryEndpoint != nil {
			database, err := newElastiCacheDatabase(cluster, nodeGroup.PrimaryEndpoint, awsutils.ElastiCachePrimaryEndpoint, extraLabels)
			if err != nil {
				return nil, trace.Wrap(err)
			}
			databases = append(databases, database)
		}

		if nodeGroup.ReaderEndpoint != nil {
			database, err := newElastiCacheDatabase(cluster, nodeGroup.ReaderEndpoint, awsutils.ElastiCacheReaderEndpoint, extraLabels)
			if err != nil {
				return nil, trace.Wrap(err)
			}
			databases = append(databases, database)
		}
	}
	return databases, nil
}

// newElastiCacheDatabase returns a new ElastiCache database.
func newElastiCacheDatabase(cluster *elasticache.ReplicationGroup, endpoint *elasticache.Endpoint, endpointType string, extraLabels map[string]string) (types.Database, error) {
	metadata, err := MetadataFromElastiCacheCluster(cluster, endpointType)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	suffix := make([]string, 0)
	if endpointType == awsutils.ElastiCacheReaderEndpoint {
		suffix = []string{endpointType}
	}

	return types.NewDatabaseV3(setDBName(types.Metadata{
		Description: fmt.Sprintf("ElastiCache cluster in %v (%v endpoint)", metadata.Region, endpointType),
		Labels:      labelsFromMetaAndEndpointType(metadata, endpointType, extraLabels),
	}, aws.StringValue(cluster.ReplicationGroupId), suffix...), types.DatabaseSpecV3{
		Protocol: defaults.ProtocolRedis,
		URI:      fmt.Sprintf("%v:%v", aws.StringValue(endpoint.Address), aws.Int64Value(endpoint.Port)),
		AWS:      *metadata,
	})
}

// NewDatabaseFromMemoryDBCluster creates a database resource from a MemoryDB
// cluster.
func NewDatabaseFromMemoryDBCluster(cluster *memorydb.Cluster, extraLabels map[string]string) (types.Database, error) {
	endpointType := awsutils.MemoryDBClusterEndpoint

	metadata, err := MetadataFromMemoryDBCluster(cluster, endpointType)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return types.NewDatabaseV3(
		setDBName(types.Metadata{
			Description: fmt.Sprintf("MemoryDB cluster in %v", metadata.Region),
			Labels:      labelsFromMetaAndEndpointType(metadata, endpointType, extraLabels),
		}, aws.StringValue(cluster.Name)),
		types.DatabaseSpecV3{
			Protocol: defaults.ProtocolRedis,
			URI:      fmt.Sprintf("%v:%v", aws.StringValue(cluster.ClusterEndpoint.Address), aws.Int64Value(cluster.ClusterEndpoint.Port)),
			AWS:      *metadata,
		})
}

// NewDatabaseFromRedshiftServerlessWorkgroup creates a database resource from
// a Redshift Serverless Workgroup.
func NewDatabaseFromRedshiftServerlessWorkgroup(workgroup *redshiftserverless.Workgroup, tags []*redshiftserverless.Tag) (types.Database, error) {
	if workgroup.Endpoint == nil {
		return nil, trace.BadParameter("missing endpoint")
	}

	metadata, err := MetadataFromRedshiftServerlessWorkgroup(workgroup)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return types.NewDatabaseV3(
		setDBName(types.Metadata{
			Description: fmt.Sprintf("Redshift Serverless workgroup in %v", metadata.Region),
			Labels:      labelsFromRedshiftServerlessWorkgroup(workgroup, metadata, tags),
		}, metadata.RedshiftServerless.WorkgroupName),
		types.DatabaseSpecV3{
			Protocol: defaults.ProtocolPostgres,
			URI:      fmt.Sprintf("%v:%v", aws.StringValue(workgroup.Endpoint.Address), aws.Int64Value(workgroup.Endpoint.Port)),
			AWS:      *metadata,
		})
}

// NewDatabaseFromRedshiftServerlessVPCEndpoint creates a database resource from
// a Redshift Serverless VPC endpoint.
func NewDatabaseFromRedshiftServerlessVPCEndpoint(endpoint *redshiftserverless.EndpointAccess, workgroup *redshiftserverless.Workgroup, tags []*redshiftserverless.Tag) (types.Database, error) {
	if workgroup.Endpoint == nil {
		return nil, trace.BadParameter("missing endpoint")
	}

	metadata, err := MetadataFromRedshiftServerlessVPCEndpoint(endpoint, workgroup)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return types.NewDatabaseV3(
		setDBName(types.Metadata{
			Description: fmt.Sprintf("Redshift Serverless endpoint in %v", metadata.Region),
			Labels:      labelsFromRedshiftServerlessVPCEndpoint(endpoint, workgroup, metadata, tags),
		}, metadata.RedshiftServerless.WorkgroupName, metadata.RedshiftServerless.EndpointName),
		types.DatabaseSpecV3{
			Protocol: defaults.ProtocolPostgres,
			URI:      fmt.Sprintf("%v:%v", aws.StringValue(endpoint.Address), aws.Int64Value(endpoint.Port)),
			AWS:      *metadata,

			// Use workgroup's default address as the server name.
			TLS: types.DatabaseTLS{
				ServerName: aws.StringValue(workgroup.Endpoint.Address),
			},
		})
}

// MetadataFromRDSInstance creates AWS metadata from the provided RDS instance.
func MetadataFromRDSInstance(rdsInstance *rds.DBInstance) (*types.AWS, error) {
	parsedARN, err := arn.Parse(aws.StringValue(rdsInstance.DBInstanceArn))
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return &types.AWS{
		Region:    parsedARN.Region,
		AccountID: parsedARN.AccountID,
		RDS: types.RDS{
			InstanceID: aws.StringValue(rdsInstance.DBInstanceIdentifier),
			ClusterID:  aws.StringValue(rdsInstance.DBClusterIdentifier),
			ResourceID: aws.StringValue(rdsInstance.DbiResourceId),
			IAMAuth:    aws.BoolValue(rdsInstance.IAMDatabaseAuthenticationEnabled),
		},
	}, nil
}

// MetadataFromRDSCluster creates AWS metadata from the provided RDS cluster.
func MetadataFromRDSCluster(rdsCluster *rds.DBCluster) (*types.AWS, error) {
	parsedARN, err := arn.Parse(aws.StringValue(rdsCluster.DBClusterArn))
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return &types.AWS{
		Region:    parsedARN.Region,
		AccountID: parsedARN.AccountID,
		RDS: types.RDS{
			ClusterID:  aws.StringValue(rdsCluster.DBClusterIdentifier),
			ResourceID: aws.StringValue(rdsCluster.DbClusterResourceId),
			IAMAuth:    aws.BoolValue(rdsCluster.IAMDatabaseAuthenticationEnabled),
		},
	}, nil
}

// MetadataFromRDSProxy creates AWS metadata from the provided RDS Proxy.
func MetadataFromRDSProxy(rdsProxy *rds.DBProxy) (*types.AWS, error) {
	parsedARN, err := arn.Parse(aws.StringValue(rdsProxy.DBProxyArn))
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// rds.DBProxy has no resource ID attribute. The resource ID can be found
	// in the ARN, e.g.:
	//
	// arn:aws:rds:ca-central-1:1234567890:db-proxy:prx-xxxyyyzzz
	//
	// In this example, the arn.Resource is "db-proxy:prx-xxxyyyzzz", where the
	// resource type is "db-proxy" and the resource ID is "prx-xxxyyyzzz".
	_, resourceID, ok := strings.Cut(parsedARN.Resource, ":")
	if !ok {
		return nil, trace.BadParameter("failed to find resource ID from %v", aws.StringValue(rdsProxy.DBProxyArn))
	}

	return &types.AWS{
		Region:    parsedARN.Region,
		AccountID: parsedARN.AccountID,
		RDSProxy: types.RDSProxy{
			Name:       aws.StringValue(rdsProxy.DBProxyName),
			ResourceID: resourceID,
		},
	}, nil
}

// MetadataFromRDSProxyCustomEndpoint creates AWS metadata from the provided
// RDS Proxy custom endpoint.
func MetadataFromRDSProxyCustomEndpoint(rdsProxy *rds.DBProxy, customEndpoint *rds.DBProxyEndpoint) (*types.AWS, error) {
	// Using resource ID from the default proxy for IAM policies to gain the
	// RDS connection access.
	metadata, err := MetadataFromRDSProxy(rdsProxy)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	metadata.RDSProxy.CustomEndpointName = aws.StringValue(customEndpoint.DBProxyEndpointName)
	return metadata, nil
}

// MetadataFromRedshiftCluster creates AWS metadata from the provided Redshift cluster.
func MetadataFromRedshiftCluster(cluster *redshift.Cluster) (*types.AWS, error) {
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

// MetadataFromElastiCacheCluster creates AWS metadata for the provided
// ElastiCache cluster.
func MetadataFromElastiCacheCluster(cluster *elasticache.ReplicationGroup, endpointType string) (*types.AWS, error) {
	parsedARN, err := arn.Parse(aws.StringValue(cluster.ARN))
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return &types.AWS{
		Region:    parsedARN.Region,
		AccountID: parsedARN.AccountID,
		ElastiCache: types.ElastiCache{
			ReplicationGroupID:       aws.StringValue(cluster.ReplicationGroupId),
			UserGroupIDs:             aws.StringValueSlice(cluster.UserGroupIds),
			TransitEncryptionEnabled: aws.BoolValue(cluster.TransitEncryptionEnabled),
			EndpointType:             endpointType,
		},
	}, nil
}

// MetadataFromMemoryDBCluster creates AWS metadata for the provided MemoryDB
// cluster.
func MetadataFromMemoryDBCluster(cluster *memorydb.Cluster, endpointType string) (*types.AWS, error) {
	parsedARN, err := arn.Parse(aws.StringValue(cluster.ARN))
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return &types.AWS{
		Region:    parsedARN.Region,
		AccountID: parsedARN.AccountID,
		MemoryDB: types.MemoryDB{
			ClusterName:  aws.StringValue(cluster.Name),
			ACLName:      aws.StringValue(cluster.ACLName),
			TLSEnabled:   aws.BoolValue(cluster.TLSEnabled),
			EndpointType: endpointType,
		},
	}, nil
}

// MetadataFromRedshiftServerlessWorkgroup creates AWS metadata for the
// provided Redshift Serverless Workgroup.
func MetadataFromRedshiftServerlessWorkgroup(workgroup *redshiftserverless.Workgroup) (*types.AWS, error) {
	parsedARN, err := arn.Parse(aws.StringValue(workgroup.WorkgroupArn))
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return &types.AWS{
		Region:    parsedARN.Region,
		AccountID: parsedARN.AccountID,
		RedshiftServerless: types.RedshiftServerless{
			WorkgroupName: aws.StringValue(workgroup.WorkgroupName),
			WorkgroupID:   aws.StringValue(workgroup.WorkgroupId),
		},
	}, nil
}

// MetadataFromRedshiftServerlessVPCEndpoint creates AWS metadata for the
// provided Redshift Serverless VPC endpoint.
func MetadataFromRedshiftServerlessVPCEndpoint(endpoint *redshiftserverless.EndpointAccess, workgroup *redshiftserverless.Workgroup) (*types.AWS, error) {
	parsedARN, err := arn.Parse(aws.StringValue(endpoint.EndpointArn))
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return &types.AWS{
		Region:    parsedARN.Region,
		AccountID: parsedARN.AccountID,
		RedshiftServerless: types.RedshiftServerless{
			WorkgroupName: aws.StringValue(endpoint.WorkgroupName),
			EndpointName:  aws.StringValue(endpoint.EndpointName),
			WorkgroupID:   aws.StringValue(workgroup.WorkgroupId),
		},
	}, nil
}

// ExtraElastiCacheLabels returns a list of extra labels for provided
// ElastiCache cluster.
func ExtraElastiCacheLabels(cluster *elasticache.ReplicationGroup, tags []*elasticache.Tag, allNodes []*elasticache.CacheCluster, allSubnetGroups []*elasticache.CacheSubnetGroup) map[string]string {
	replicationGroupID := aws.StringValue(cluster.ReplicationGroupId)
	subnetGroupName := ""
	labels := make(map[string]string)

	// Find any node belongs to this cluster and set engine version label.
	for _, node := range allNodes {
		if aws.StringValue(node.ReplicationGroupId) == replicationGroupID {
			subnetGroupName = aws.StringValue(node.CacheSubnetGroupName)
			labels[labelEngineVersion] = aws.StringValue(node.EngineVersion)
			break
		}
	}

	// Find the subnet group used by this cluster and set VPC ID label.
	//
	// ElastiCache servers do not have public IPs so they are usually only
	// accessible within the same VPC. Having a VPC ID label can be very useful
	// for filtering.
	for _, subnetGroup := range allSubnetGroups {
		if aws.StringValue(subnetGroup.CacheSubnetGroupName) == subnetGroupName {
			labels[labelVPCID] = aws.StringValue(subnetGroup.VpcId)
			break
		}
	}

	// Add AWS resource tags.
	return addLabels(labels, libcloudaws.TagsToLabels(tags))
}

// ExtraMemoryDBLabels returns a list of extra labels for provided MemoryDB
// cluster.
func ExtraMemoryDBLabels(cluster *memorydb.Cluster, tags []*memorydb.Tag, allSubnetGroups []*memorydb.SubnetGroup) map[string]string {
	labels := make(map[string]string)

	// Engine version.
	labels[labelEngineVersion] = aws.StringValue(cluster.EngineVersion)

	// VPC ID.
	for _, subnetGroup := range allSubnetGroups {
		if aws.StringValue(subnetGroup.Name) == aws.StringValue(cluster.SubnetGroupName) {
			labels[labelVPCID] = aws.StringValue(subnetGroup.VpcId)
			break
		}
	}

	// Add AWS resource tags.
	return addLabels(labels, libcloudaws.TagsToLabels(tags))
}

// rdsEngineToProtocol converts RDS instance engine to the database protocol.
func rdsEngineToProtocol(engine string) (string, error) {
	switch engine {
	case RDSEnginePostgres, RDSEngineAuroraPostgres:
		return defaults.ProtocolPostgres, nil
	case RDSEngineMySQL, RDSEngineAurora, RDSEngineAuroraMySQL, RDSEngineMariaDB:
		return defaults.ProtocolMySQL, nil
	}
	return "", trace.BadParameter("unknown RDS engine type %q", engine)
}

// rdsEngineFamilyToProtocol converts RDS engine family to the database protocol.
func rdsEngineFamilyToProtocol(engineFamily string) (string, error) {
	switch engineFamily {
	case rds.EngineFamilyMysql:
		return defaults.ProtocolMySQL, nil
	case rds.EngineFamilyPostgresql:
		return defaults.ProtocolPostgres, nil
	case rds.EngineFamilySqlserver:
		return defaults.ProtocolSQLServer, nil
	}
	return "", trace.BadParameter("unknown RDS engine family type %q", engineFamily)
}

// labelsFromAzureServer creates database labels for the provided Azure DB server.
func labelsFromAzureServer(server *azure.DBServer) (map[string]string, error) {
	labels := azureTagsToLabels(server.Tags)
	labels[types.OriginLabel] = types.OriginCloud
	labels[labelRegion] = server.Location
	labels[labelEngineVersion] = server.Properties.Version
	return withLabelsFromAzureResourceID(labels, server.ID)
}

// withLabelsFromAzureResourceID adds labels extracted from the Azure resource ID.
func withLabelsFromAzureResourceID(labels map[string]string, resourceID string) (map[string]string, error) {
	rid, err := arm.ParseResourceID(resourceID)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	labels[labelEngine] = rid.ResourceType.String()
	labels[labelResourceGroup] = rid.ResourceGroupName
	labels[labelSubscriptionID] = rid.SubscriptionID
	return labels, nil
}

// labelsFromAzureRedis creates database labels from the provided Azure Redis instance.
func labelsFromAzureRedis(server *armredis.ResourceInfo) (map[string]string, error) {
	labels := azureTagsToLabels(azure.ConvertTags(server.Tags))
	labels[types.OriginLabel] = types.OriginCloud
	labels[labelRegion] = azure.StringVal(server.Location)
	labels[labelEngineVersion] = azure.StringVal(server.Properties.RedisVersion)
	return withLabelsFromAzureResourceID(labels, azure.StringVal(server.ID))
}

// labelsFromAzureRedisEnterprise creates database labels from the provided Azure Redis Enterprise server.
func labelsFromAzureRedisEnterprise(cluster *armredisenterprise.Cluster, database *armredisenterprise.Database) (map[string]string, error) {
	labels := azureTagsToLabels(azure.ConvertTags(cluster.Tags))
	labels[types.OriginLabel] = types.OriginCloud
	labels[labelRegion] = azure.StringVal(cluster.Location)
	labels[labelEngineVersion] = azure.StringVal(cluster.Properties.RedisVersion)
	labels[labelEndpointType] = azure.StringVal(database.Properties.ClusteringPolicy)
	return withLabelsFromAzureResourceID(labels, azure.StringVal(cluster.ID))
}

// labelsFromAzureSQLServer creates database labels from the provided Azure SQL
// server.
func labelsFromAzureSQLServer(server *armsql.Server) (map[string]string, error) {
	labels := azureTagsToLabels(azure.ConvertTags(server.Tags))
	labels[types.OriginLabel] = types.OriginCloud
	labels[labelRegion] = azure.StringVal(server.Location)
	labels[labelEngineVersion] = azure.StringVal(server.Properties.Version)
	return withLabelsFromAzureResourceID(labels, azure.StringVal(server.ID))
}

// labelsFromAzureManagedSQLServer creates database labels from the provided
// Azure Managed SQL server.
func labelsFromAzureManagedSQLServer(server *armsql.ManagedInstance) (map[string]string, error) {
	labels := azureTagsToLabels(azure.ConvertTags(server.Tags))
	labels[types.OriginLabel] = types.OriginCloud
	labels[labelRegion] = azure.StringVal(server.Location)
	return withLabelsFromAzureResourceID(labels, azure.StringVal(server.ID))
}

// labelsFromRDSInstance creates database labels for the provided RDS instance.
func labelsFromRDSInstance(rdsInstance *rds.DBInstance, meta *types.AWS) map[string]string {
	labels := labelsFromAWSMetadata(meta)
	labels[labelEngine] = aws.StringValue(rdsInstance.Engine)
	labels[labelEngineVersion] = aws.StringValue(rdsInstance.EngineVersion)
	labels[labelEndpointType] = string(RDSEndpointTypeInstance)
	return addLabels(labels, libcloudaws.TagsToLabels(rdsInstance.TagList))
}

// labelsFromRDSCluster creates database labels for the provided RDS cluster.
func labelsFromRDSCluster(rdsCluster *rds.DBCluster, meta *types.AWS, endpointType RDSEndpointType) map[string]string {
	labels := labelsFromAWSMetadata(meta)
	labels[labelEngine] = aws.StringValue(rdsCluster.Engine)
	labels[labelEngineVersion] = aws.StringValue(rdsCluster.EngineVersion)
	labels[labelEndpointType] = string(endpointType)
	return addLabels(labels, libcloudaws.TagsToLabels(rdsCluster.TagList))
}

// labelsFromRDSProxy creates database labels for the provided RDS Proxy.
func labelsFromRDSProxy(rdsProxy *rds.DBProxy, meta *types.AWS, tags []*rds.Tag) map[string]string {
	// rds.DBProxy has no TagList.
	labels := labelsFromAWSMetadata(meta)
	labels[labelVPCID] = aws.StringValue(rdsProxy.VpcId)
	labels[labelEngine] = aws.StringValue(rdsProxy.EngineFamily)
	return addLabels(labels, libcloudaws.TagsToLabels(tags))
}

// labelsFromRDSProxyCustomEndpoint creates database labels for the provided
// RDS Proxy custom endpoint.
func labelsFromRDSProxyCustomEndpoint(rdsProxy *rds.DBProxy, customEndpoint *rds.DBProxyEndpoint, meta *types.AWS, tags []*rds.Tag) map[string]string {
	labels := labelsFromRDSProxy(rdsProxy, meta, tags)
	labels[labelEndpointType] = aws.StringValue(customEndpoint.TargetRole)
	return labels
}

// labelsFromRedshiftCluster creates database labels for the provided Redshift cluster.
func labelsFromRedshiftCluster(cluster *redshift.Cluster, meta *types.AWS) map[string]string {
	labels := labelsFromAWSMetadata(meta)
	return addLabels(labels, libcloudaws.TagsToLabels(cluster.Tags))
}

func labelsFromRedshiftServerlessWorkgroup(workgroup *redshiftserverless.Workgroup, meta *types.AWS, tags []*redshiftserverless.Tag) map[string]string {
	labels := labelsFromAWSMetadata(meta)
	labels[labelEndpointType] = RedshiftServerlessWorkgroupEndpoint
	labels[labelNamespace] = aws.StringValue(workgroup.NamespaceName)
	if workgroup.Endpoint != nil && len(workgroup.Endpoint.VpcEndpoints) > 0 {
		labels[labelVPCID] = aws.StringValue(workgroup.Endpoint.VpcEndpoints[0].VpcId)
	}
	return addLabels(labels, libcloudaws.TagsToLabels(tags))
}

func labelsFromRedshiftServerlessVPCEndpoint(endpoint *redshiftserverless.EndpointAccess, workgroup *redshiftserverless.Workgroup, meta *types.AWS, tags []*redshiftserverless.Tag) map[string]string {
	labels := labelsFromAWSMetadata(meta)
	labels[labelEndpointType] = RedshiftServerlessVPCEndpoint
	labels[labelWorkgroup] = aws.StringValue(endpoint.WorkgroupName)
	labels[labelNamespace] = aws.StringValue(workgroup.NamespaceName)
	if endpoint.VpcEndpoint != nil {
		labels[labelVPCID] = aws.StringValue(endpoint.VpcEndpoint.VpcId)
	}
	return addLabels(labels, libcloudaws.TagsToLabels(tags))
}

// labelsFromAWSMetadata returns labels from provided AWS metadata.
func labelsFromAWSMetadata(meta *types.AWS) map[string]string {
	labels := make(map[string]string)
	labels[types.OriginLabel] = types.OriginCloud
	if meta != nil {
		labels[labelAccountID] = meta.AccountID
		labels[labelRegion] = meta.Region
	}
	return labels
}

// labelsFromMetaAndEndpointType creates database labels from provided AWS meta and endpoint type.
func labelsFromMetaAndEndpointType(meta *types.AWS, endpointType string, extraLabels map[string]string) map[string]string {
	labels := labelsFromAWSMetadata(meta)
	labels[labelEndpointType] = endpointType
	return addLabels(labels, extraLabels)
}

// azureTagsToLabels converts Azure tags to a labels map.
func azureTagsToLabels(tags map[string]string) map[string]string {
	labels := make(map[string]string)
	return addLabels(labels, tags)
}

func addLabels(labels map[string]string, moreLabels map[string]string) map[string]string {
	for key, value := range moreLabels {
		if types.IsValidLabelKey(key) {
			labels[key] = value
		} else {
			log.Debugf("Skipping %q, not a valid label key.", key)
		}
	}
	return labels
}

// IsRDSInstanceSupported returns true if database supports IAM authentication.
// Currently, only MariaDB is being checked.
func IsRDSInstanceSupported(instance *rds.DBInstance) bool {
	// TODO(jakule): Check other engines.
	if aws.StringValue(instance.Engine) != RDSEngineMariaDB {
		return true
	}

	// MariaDB follows semver schema: https://mariadb.org/about/
	ver, err := semver.NewVersion(aws.StringValue(instance.EngineVersion))
	if err != nil {
		log.Errorf("Failed to parse RDS MariaDB version: %s", aws.StringValue(instance.EngineVersion))
		return false
	}

	// Min supported MariaDB version that supports IAM is 10.6
	// https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/UsingWithRDS.IAMDBAuth.html
	minIAMSupportedVer := semver.New("10.6.0")
	return !ver.LessThan(*minIAMSupportedVer)
}

// IsRDSClusterSupported checks whether the Aurora cluster is supported.
func IsRDSClusterSupported(cluster *rds.DBCluster) bool {
	switch aws.StringValue(cluster.EngineMode) {
	// Aurora Serverless v1 does NOT support IAM authentication.
	// https://docs.aws.amazon.com/AmazonRDS/latest/AuroraUserGuide/aurora-serverless.html#aurora-serverless.limitations
	//
	// Note that Aurora Serverless v2 does support IAM authentication.
	// https://docs.aws.amazon.com/AmazonRDS/latest/AuroraUserGuide/aurora-serverless-v2.html
	// However, v2's engine mode is "provisioned" instead of "serverless" so it
	// goes to the default case (true).
	case RDSEngineModeServerless:
		return false

	// Aurora MySQL 1.22.2, 1.20.1, 1.19.6, and 5.6.10a only: Parallel query doesn't support AWS Identity and Access Management (IAM) database authentication.
	// https://docs.aws.amazon.com/AmazonRDS/latest/AuroraUserGuide/aurora-mysql-parallel-query.html#aurora-mysql-parallel-query-limitations
	case RDSEngineModeParallelQuery:
		if slices.Contains([]string{"1.22.2", "1.20.1", "1.19.6", "5.6.10a"}, auroraMySQLVersion(cluster)) {
			return false
		}
	}

	return true
}

// IsElastiCacheClusterSupported checks whether the ElastiCache cluster is
// supported.
func IsElastiCacheClusterSupported(cluster *elasticache.ReplicationGroup) bool {
	return aws.BoolValue(cluster.TransitEncryptionEnabled)
}

// IsMemoryDBClusterSupported checks whether the MemoryDB cluster is supported.
func IsMemoryDBClusterSupported(cluster *memorydb.Cluster) bool {
	return aws.BoolValue(cluster.TLSEnabled)
}

// IsRDSInstanceAvailable checks if the RDS instance is available.
func IsRDSInstanceAvailable(instance *rds.DBInstance) bool {
	// For a full list of status values, see:
	// https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/accessing-monitoring.html
	switch aws.StringValue(instance.DBInstanceStatus) {
	// Statuses marked as "Billed" in the above guide.
	case "available", "backing-up", "configuring-enhanced-monitoring",
		"configuring-iam-database-auth", "configuring-log-exports",
		"converting-to-vpc", "incompatible-option-group",
		"incompatible-parameters", "maintenance", "modifying", "moving-to-vpc",
		"rebooting", "resetting-master-credentials", "renaming", "restore-error",
		"storage-full", "storage-optimization", "upgrading":
		return true

	// Statuses marked as "Not billed" in the above guide.
	case "creating", "deleting", "failed",
		"inaccessible-encryption-credentials", "incompatible-network",
		"incompatible-restore":
		return false

	// Statuses marked as "Billed for storage" in the above guide.
	case "inaccessible-encryption-credentials-recoverable", "starting",
		"stopped", "stopping":
		return false

	// Statuses that have no billing information in the above guide, but
	// believed to be unavailable.
	case "insufficient-capacity":
		return false

	default:
		log.Warnf("Unknown status type: %q. Assuming RDS instance %q is available.",
			aws.StringValue(instance.DBInstanceStatus),
			aws.StringValue(instance.DBInstanceIdentifier),
		)
		return true
	}
}

// IsRDSClusterAvailable checks if the RDS cluster is available.
func IsRDSClusterAvailable(cluster *rds.DBCluster) bool {
	// For a full list of status values, see:
	// https://docs.aws.amazon.com/AmazonRDS/latest/AuroraUserGuide/accessing-monitoring.html
	switch aws.StringValue(cluster.Status) {
	// Statuses marked as "Billed" in the above guide.
	case "available", "backing-up", "backtracking", "failing-over",
		"maintenance", "migrating", "modifying", "promoting", "renaming",
		"resetting-master-credentials", "update-iam-db-auth", "upgrading":
		return true

	// Statuses marked as "Not billed" in the above guide.
	case "cloning-failed", "creating", "deleting",
		"inaccessible-encryption-credentials", "migration-failed":
		return false

	// Statuses marked as "Billed for storage" in the above guide.
	case "starting", "stopped", "stopping":
		return false

	default:
		log.Warnf("Unknown status type: %q. Assuming Aurora cluster %q is available.",
			aws.StringValue(cluster.Status),
			aws.StringValue(cluster.DBClusterIdentifier),
		)
		return true
	}
}

// IsRedshiftClusterAvailable checks if the Redshift cluster is available.
func IsRedshiftClusterAvailable(cluster *redshift.Cluster) bool {
	// For a full list of status values, see:
	// https://docs.aws.amazon.com/redshift/latest/mgmt/working-with-clusters.html#rs-mgmt-cluster-status
	//
	// Note that the Redshift guide does not specify billing information like
	// the RDS and Aurora guides do. Most Redshift statuses are
	// cross-referenced with similar statuses from RDS and Aurora guides to
	// determine the availability.
	//
	// For "incompatible-xxx" statuses, the cluster is assumed to be available
	// if the status is resulted by modifying the cluster, and the cluster is
	// assumed to be unavailable if the cluster cannot be created or restored.
	switch aws.StringValue(cluster.ClusterStatus) {
	// nolint:misspell
	case "available", "available, prep-for-resize", "available, resize-cleanup",
		"cancelling-resize", "final-snapshot", "modifying", "rebooting",
		"renaming", "resizing", "rotating-keys", "storage-full", "updating-hsm",
		"incompatible-parameters", "incompatible-hsm":
		return true

	case "creating", "deleting", "hardware-failure", "paused",
		"incompatible-network":
		return false

	default:
		log.Warnf("Unknown status type: %q. Assuming Redshift cluster %q is available.",
			aws.StringValue(cluster.ClusterStatus),
			aws.StringValue(cluster.ClusterIdentifier),
		)
		return true
	}
}

// IsAWSResourceAvailable checks if the input status indicates the resource is
// available for use.
//
// Note that this function checks some common values but not necessarily covers
// everything. For types that have other known status values, separate
// functions (e.g. IsRDSClusterAvailable) can be implemented.
func IsAWSResourceAvailable(r interface{}, status *string) bool {
	switch strings.ToLower(aws.StringValue(status)) {
	case "available", "modifying", "snapshotting":
		return true

	case "creating", "deleting", "create-failed":
		return false

	default:
		log.WithField("aws_resource", r).Warnf("Unknown status type: %q. Assuming the AWS resource %T is available.", aws.StringValue(status), r)
		return true
	}
}

// IsElastiCacheClusterAvailable checks if the ElastiCache cluster is
// available.
func IsElastiCacheClusterAvailable(cluster *elasticache.ReplicationGroup) bool {
	return IsAWSResourceAvailable(cluster, cluster.Status)
}

// IsMemoryDBClusterAvailable checks if the MemoryDB cluster is available.
func IsMemoryDBClusterAvailable(cluster *memorydb.Cluster) bool {
	return IsAWSResourceAvailable(cluster, cluster.Status)
}

// IsRDSProxyAvailable checks if the RDS Proxy is available.
func IsRDSProxyAvailable(dbProxy *rds.DBProxy) bool {
	return IsAWSResourceAvailable(dbProxy, dbProxy.Status)
}

// IsRDSProxyCustomEndpointAvailable checks if the RDS Proxy custom endpoint is available.
func IsRDSProxyCustomEndpointAvailable(customEndpoint *rds.DBProxyEndpoint) bool {
	return IsAWSResourceAvailable(customEndpoint, customEndpoint.Status)
}

// auroraMySQLVersion extracts aurora mysql version from engine version
func auroraMySQLVersion(cluster *rds.DBCluster) string {
	// version guide: https://docs.aws.amazon.com/AmazonRDS/latest/AuroraUserGuide/AuroraMySQL.Updates.Versions.html
	// a list of all the available versions: https://docs.aws.amazon.com/cli/latest/reference/rds/describe-db-engine-versions.html
	//
	// some examples of possible inputs:
	// 5.6.10a
	// 5.7.12
	// 5.6.mysql_aurora.1.22.0
	// 5.6.mysql_aurora.1.22.1
	// 5.6.mysql_aurora.1.22.1.3
	//
	// general format is: <mysql-major-version>.mysql_aurora.<aurora-mysql-version>
	// 5.6.10a and 5.7.12 are "legacy" versions and they are returned as it is
	version := aws.StringValue(cluster.EngineVersion)
	parts := strings.Split(version, ".mysql_aurora.")
	if len(parts) == 2 {
		return parts[1]
	}
	return version
}

// GetMySQLEngineVersion returns MySQL engine version from provided metadata labels.
// An empty string is returned if label doesn't exist.
func GetMySQLEngineVersion(labels map[string]string) string {
	if engine, ok := labels[labelEngine]; !ok || (engine != RDSEngineMySQL && engine != AzureEngineMySQL) {
		return ""
	}

	version, ok := labels[labelEngineVersion]
	if !ok {
		return ""
	}
	return version
}

const (
	// labelAccountID is the label key containing AWS account ID.
	labelAccountID = "account-id"
	// labelRegion is the label key containing AWS region.
	labelRegion = "region"
	// labelEngine is the label key containing RDS database engine name.
	labelEngine = "engine"
	// labelEngineVersion is the label key containing RDS database engine version.
	labelEngineVersion = "engine-version"
	// labelEndpointType is the label key containing the RDS endpoint type.
	labelEndpointType = "endpoint-type"
	// labelVPCID is the label key containing the VPC ID.
	labelVPCID = "vpc-id"
	// labelNamespace is the label key for namespace name.
	labelNamespace = "namespace"
	// labelWorkgroup is the label key for workgroup name.
	labelWorkgroup = "workgroup"
	// labelTeleportDBName is the label key containing the database name override.
	labelTeleportDBName = types.TeleportNamespace + "/database_name"
	// labelTeleportDBNameAzure is the label key containing the database name
	// override for Azure databases. Azure tags connot contain these
	// characters: "<>%&\?/".
	labelTeleportDBNameAzure = "TeleportDatabaseName"
)

const (
	// RDSEngineMySQL is RDS engine name for MySQL instances.
	RDSEngineMySQL = "mysql"
	// RDSEnginePostgres is RDS engine name for Postgres instances.
	RDSEnginePostgres = "postgres"
	// RDSEngineMariaDB is RDS engine name for MariaDB instances.
	RDSEngineMariaDB = "mariadb"
	// RDSEngineAurora is RDS engine name for Aurora MySQL 5.6 compatible clusters.
	RDSEngineAurora = "aurora"
	// RDSEngineAuroraMySQL is RDS engine name for Aurora MySQL 5.7 compatible clusters.
	RDSEngineAuroraMySQL = "aurora-mysql"
	// RDSEngineAuroraPostgres is RDS engine name for Aurora Postgres clusters.
	RDSEngineAuroraPostgres = "aurora-postgresql"
)

// RDSEndpointType specifies the endpoint type for RDS clusters.
type RDSEndpointType string

const (
	// RDSEndpointTypePrimary is the endpoint that specifies the connection for the primary instance of the RDS cluster.
	RDSEndpointTypePrimary RDSEndpointType = "primary"
	// RDSEndpointTypeReader is the endpoint that load-balances connections across the Aurora Replicas that are
	// available in an RDS cluster.
	RDSEndpointTypeReader RDSEndpointType = "reader"
	// RDSEndpointTypeCustom is the endpoint that specifies one of the custom endpoints associated with the RDS cluster.
	RDSEndpointTypeCustom RDSEndpointType = "custom"
	// RDSEndpointTypeInstance is the endpoint of an RDS DB instance.
	RDSEndpointTypeInstance RDSEndpointType = "instance"
)

const (
	// RDSEngineModeProvisioned is the RDS engine mode for provisioned Aurora clusters
	RDSEngineModeProvisioned = "provisioned"
	// RDSEngineModeServerless is the RDS engine mode for Aurora Serverless DB clusters
	RDSEngineModeServerless = "serverless"
	// RDSEngineModeParallelQuery is the RDS engine mode for Aurora MySQL clusters with parallel query enabled
	RDSEngineModeParallelQuery = "parallelquery"
	// RDSEngineModeGlobal is the RDS engine mode for Aurora Global databases
	RDSEngineModeGlobal = "global"
	// RDSEngineModeMultiMaster is the RDS engine mode for Multi-master clusters
	RDSEngineModeMultiMaster = "multimaster"
)

const (
	// AzureEngineMySQL is the Azure engine name for MySQL single-server instances
	AzureEngineMySQL = "Microsoft.DBforMySQL/servers"
	// AzureEnginePostgres is the Azure engine name for PostgreSQL single-server instances
	AzureEnginePostgres = "Microsoft.DBforPostgreSQL/servers"
)

const (
	// RedshiftServerlessWorkgroupEndpoint is the endpoint type for workgroups.
	RedshiftServerlessWorkgroupEndpoint = "workgroup"
	// RedshiftServerlessVPCEndpoint is the endpoint type for VCP endpoints.
	RedshiftServerlessVPCEndpoint = "vpc-endpoint"
)

const (
	// labelSubscriptionID is the label key for Azure subscription ID.
	labelSubscriptionID = "subscription-id"
	// labelResourceGroup is the label key for the Azure resource group name.
	labelResourceGroup = "resource-group"
)

const (
	// azureSQLServerDefaultPort is the default port for Azure SQL Server.
	azureSQLServerDefaultPort = 1433
)
