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
	"io"
	"sync"

	gcpcredentials "cloud.google.com/go/iam/credentials/apiv1"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v2"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/mysql/armmysql"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresql"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/subscription/armsubscription"
	"github.com/aws/aws-sdk-go/aws"
	awssession "github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2/ec2iface"
	"github.com/aws/aws-sdk-go/service/eks"
	"github.com/aws/aws-sdk-go/service/eks/eksiface"
	"github.com/aws/aws-sdk-go/service/elasticache"
	"github.com/aws/aws-sdk-go/service/elasticache/elasticacheiface"
	"github.com/aws/aws-sdk-go/service/iam"
	"github.com/aws/aws-sdk-go/service/iam/iamiface"
	"github.com/aws/aws-sdk-go/service/memorydb"
	"github.com/aws/aws-sdk-go/service/memorydb/memorydbiface"
	"github.com/aws/aws-sdk-go/service/rds"
	"github.com/aws/aws-sdk-go/service/rds/rdsiface"
	"github.com/aws/aws-sdk-go/service/redshift"
	"github.com/aws/aws-sdk-go/service/redshift/redshiftiface"
	"github.com/aws/aws-sdk-go/service/secretsmanager"
	"github.com/aws/aws-sdk-go/service/secretsmanager/secretsmanageriface"
	"github.com/aws/aws-sdk-go/service/ssm"
	"github.com/aws/aws-sdk-go/service/ssm/ssmiface"
	"github.com/aws/aws-sdk-go/service/sts"
	"github.com/aws/aws-sdk-go/service/sts/stsiface"
	"github.com/gravitational/trace"
	"github.com/sirupsen/logrus"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/zmb3/teleport/lib/cloud/azure"
	"github.com/zmb3/teleport/lib/cloud/gcp"
)

// Clients provides interface for obtaining cloud provider clients.
type Clients interface {
	// GetAWSSession returns AWS session for the specified region.
	GetAWSSession(region string) (*awssession.Session, error)
	// GetAWSRDSClient returns AWS RDS client for the specified region.
	GetAWSRDSClient(region string) (rdsiface.RDSAPI, error)
	// GetAWSRedshiftClient returns AWS Redshift client for the specified region.
	GetAWSRedshiftClient(region string) (redshiftiface.RedshiftAPI, error)
	// GetAWSElastiCacheClient returns AWS ElastiCache client for the specified region.
	GetAWSElastiCacheClient(region string) (elasticacheiface.ElastiCacheAPI, error)
	// GetAWSMemoryDBClient returns AWS MemoryDB client for the specified region.
	GetAWSMemoryDBClient(region string) (memorydbiface.MemoryDBAPI, error)
	// GetAWSSecretsManagerClient returns AWS Secrets Manager client for the specified region.
	GetAWSSecretsManagerClient(region string) (secretsmanageriface.SecretsManagerAPI, error)
	// GetAWSIAMClient returns AWS IAM client for the specified region.
	GetAWSIAMClient(region string) (iamiface.IAMAPI, error)
	// GetAWSSTSClient returns AWS STS client for the specified region.
	GetAWSSTSClient(region string) (stsiface.STSAPI, error)
	// GetAWSEC2Client returns AWS EC2 client for the specified region.
	GetAWSEC2Client(region string) (ec2iface.EC2API, error)
	// GetAWSSSMClient returns AWS SSM client for the specified region.
	GetAWSSSMClient(region string) (ssmiface.SSMAPI, error)
	// GetAWSEKSClient returns AWS EKS client for the specified region.
	GetAWSEKSClient(region string) (eksiface.EKSAPI, error)
	// GetGCPIAMClient returns GCP IAM client.
	GetGCPIAMClient(context.Context) (*gcpcredentials.IamCredentialsClient, error)
	// GetGCPSQLAdminClient returns GCP Cloud SQL Admin client.
	GetGCPSQLAdminClient(context.Context) (gcp.SQLAdminClient, error)
	// GetInstanceMetadataClient returns instance metadata client based on which
	// cloud provider Teleport is running on, if any.
	GetInstanceMetadataClient(ctx context.Context) (InstanceMetadata, error)
	// GetGCPGKEClient returns GKE client.
	GetGCPGKEClient(context.Context) (gcp.GKEClient, error)
	// AzureClients is an interface for Azure-specific API clients
	AzureClients
	// Closer closes all initialized clients.
	io.Closer
}

// AzureClients is an interface for Azure-specific API clients
type AzureClients interface {
	// GetAzureCredential returns Azure default token credential chain.
	GetAzureCredential() (azcore.TokenCredential, error)
	// GetAzureMySQLClient returns Azure MySQL client for the specified subscription.
	GetAzureMySQLClient(subscription string) (azure.DBServersClient, error)
	// GetAzurePostgresClient returns Azure Postgres client for the specified subscription.
	GetAzurePostgresClient(subscription string) (azure.DBServersClient, error)
	// GetAzureSubscriptionClient returns an Azure Subscriptions client
	GetAzureSubscriptionClient() (*azure.SubscriptionClient, error)
	// GetAzureRedisClient returns an Azure Redis client for the given subscription.
	GetAzureRedisClient(subscription string) (azure.RedisClient, error)
	// GetAzureRedisEnterpriseClient returns an Azure Redis Enterprise client for the given subscription.
	GetAzureRedisEnterpriseClient(subscription string) (azure.RedisEnterpriseClient, error)
	// GetAzureKubernetesClient returns an Azure AKS client for the specified subscription.
	GetAzureKubernetesClient(subscription string) (azure.AKSClient, error)
	// GetAzureVirtualMachinesClient returns an Azure Virtual Machines client for the given subscription.
	GetAzureVirtualMachinesClient(subscription string) (azure.VirtualMachinesClient, error)
}

// NewClients returns a new instance of cloud clients retriever.
func NewClients() Clients {
	return &cloudClients{
		awsSessions: make(map[string]*awssession.Session),
		azureClients: azureClients{
			azureMySQLClients:           make(map[string]azure.DBServersClient),
			azurePostgresClients:        make(map[string]azure.DBServersClient),
			azureRedisClients:           azure.NewClientMap(azure.NewRedisClient),
			azureRedisEnterpriseClients: azure.NewClientMap(azure.NewRedisEnterpriseClient),
			azureKubernetesClient:       make(map[string]azure.AKSClient),
			azureVirtualMachinesClients: azure.NewClientMap(azure.NewVirtualMachinesClient),
		},
	}
}

// cloudClients implements Clients
var _ Clients = (*cloudClients)(nil)

type cloudClients struct {
	// awsSessions is a map of cached AWS sessions per region.
	awsSessions map[string]*awssession.Session
	// gcpIAM is the cached GCP IAM client.
	gcpIAM *gcpcredentials.IamCredentialsClient
	// gcpSQLAdmin is the cached GCP Cloud SQL Admin client.
	gcpSQLAdmin gcp.SQLAdminClient
	// instanceMetadata is the cached instance metadata client.
	instanceMetadata InstanceMetadata
	// gcpGKE is the cached GCP Cloud GKE client.
	gcpGKE gcp.GKEClient
	// azureClients contains Azure-specific clients.
	azureClients
	// mtx is used for locking.
	mtx sync.RWMutex
}

// azureClients contains Azure-specific clients.
type azureClients struct {
	// azureCredential is the cached Azure credential.
	azureCredential azcore.TokenCredential
	// azureMySQLClients is the cached Azure MySQL Server clients.
	azureMySQLClients map[string]azure.DBServersClient
	// azurePostgresClients is the cached Azure Postgres Server clients.
	azurePostgresClients map[string]azure.DBServersClient
	// azureSubscriptionsClient is the cached Azure Subscriptions client.
	azureSubscriptionsClient *azure.SubscriptionClient
	// azureRedisClients contains the cached Azure Redis clients.
	azureRedisClients azure.ClientMap[azure.RedisClient]
	// azureRedisEnterpriseClients contains the cached Azure Redis Enterprise clients.
	azureRedisEnterpriseClients azure.ClientMap[azure.RedisEnterpriseClient]
	// azureKubernetesClient is the cached Azure Kubernetes client.
	azureKubernetesClient map[string]azure.AKSClient
	// azureVirtualMachinesClients contains the cached Azure Virtual Machines clients.
	azureVirtualMachinesClients azure.ClientMap[azure.VirtualMachinesClient]
}

// GetAWSSession returns AWS session for the specified region.
func (c *cloudClients) GetAWSSession(region string) (*awssession.Session, error) {
	c.mtx.RLock()
	if session, ok := c.awsSessions[region]; ok {
		c.mtx.RUnlock()
		return session, nil
	}
	c.mtx.RUnlock()
	return c.initAWSSession(region)
}

// GetAWSRDSClient returns AWS RDS client for the specified region.
func (c *cloudClients) GetAWSRDSClient(region string) (rdsiface.RDSAPI, error) {
	session, err := c.GetAWSSession(region)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return rds.New(session), nil
}

// GetAWSRedshiftClient returns AWS Redshift client for the specified region.
func (c *cloudClients) GetAWSRedshiftClient(region string) (redshiftiface.RedshiftAPI, error) {
	session, err := c.GetAWSSession(region)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return redshift.New(session), nil
}

// GetAWSElastiCacheClient returns AWS ElastiCache client for the specified region.
func (c *cloudClients) GetAWSElastiCacheClient(region string) (elasticacheiface.ElastiCacheAPI, error) {
	session, err := c.GetAWSSession(region)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return elasticache.New(session), nil
}

// GetAWSMemoryDBClient returns AWS MemoryDB client for the specified region.
func (c *cloudClients) GetAWSMemoryDBClient(region string) (memorydbiface.MemoryDBAPI, error) {
	session, err := c.GetAWSSession(region)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return memorydb.New(session), nil
}

// GetAWSSecretsManagerClient returns AWS Secrets Manager client for the specified region.
func (c *cloudClients) GetAWSSecretsManagerClient(region string) (secretsmanageriface.SecretsManagerAPI, error) {
	session, err := c.GetAWSSession(region)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return secretsmanager.New(session), nil
}

// GetAWSIAMClient returns AWS IAM client for the specified region.
func (c *cloudClients) GetAWSIAMClient(region string) (iamiface.IAMAPI, error) {
	session, err := c.GetAWSSession(region)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return iam.New(session), nil
}

// GetAWSSTSClient returns AWS STS client for the specified region.
func (c *cloudClients) GetAWSSTSClient(region string) (stsiface.STSAPI, error) {
	session, err := c.GetAWSSession(region)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return sts.New(session), nil
}

// GetAWSEC2Client returns AWS EC2 client for the specified region.
func (c *cloudClients) GetAWSEC2Client(region string) (ec2iface.EC2API, error) {
	session, err := c.GetAWSSession(region)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return ec2.New(session), nil
}

// GetAWSSSMClient returns AWS SSM client for the specified region.
func (c *cloudClients) GetAWSSSMClient(region string) (ssmiface.SSMAPI, error) {
	session, err := c.GetAWSSession(region)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return ssm.New(session), nil
}

// GetAWSEKSClient returns AWS EKS client for the specified region.
func (c *cloudClients) GetAWSEKSClient(region string) (eksiface.EKSAPI, error) {
	session, err := c.GetAWSSession(region)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return eks.New(session), nil
}

// GetGCPIAMClient returns GCP IAM client.
func (c *cloudClients) GetGCPIAMClient(ctx context.Context) (*gcpcredentials.IamCredentialsClient, error) {
	c.mtx.RLock()
	if c.gcpIAM != nil {
		defer c.mtx.RUnlock()
		return c.gcpIAM, nil
	}
	c.mtx.RUnlock()
	return c.initGCPIAMClient(ctx)
}

// GetGCPSQLAdminClient returns GCP Cloud SQL Admin client.
func (c *cloudClients) GetGCPSQLAdminClient(ctx context.Context) (gcp.SQLAdminClient, error) {
	c.mtx.RLock()
	if c.gcpSQLAdmin != nil {
		defer c.mtx.RUnlock()
		return c.gcpSQLAdmin, nil
	}
	c.mtx.RUnlock()
	return c.initGCPSQLAdminClient(ctx)
}

// GetInstanceMetadata returns the instance metadata.
func (c *cloudClients) GetInstanceMetadataClient(ctx context.Context) (InstanceMetadata, error) {
	c.mtx.RLock()
	if c.instanceMetadata != nil {
		defer c.mtx.RUnlock()
		return c.instanceMetadata, nil
	}
	c.mtx.RUnlock()
	return c.initInstanceMetadata(ctx)
}

// GetGCPGKEClient returns GKE client.
func (c *cloudClients) GetGCPGKEClient(ctx context.Context) (gcp.GKEClient, error) {
	c.mtx.RLock()
	if c.gcpGKE != nil {
		defer c.mtx.RUnlock()
		return c.gcpGKE, nil
	}
	c.mtx.RUnlock()
	return c.initGCPGKEClient(ctx)
}

// GetAzureCredential returns default Azure token credential chain.
func (c *cloudClients) GetAzureCredential() (azcore.TokenCredential, error) {
	c.mtx.RLock()
	if c.azureCredential != nil {
		defer c.mtx.RUnlock()
		return c.azureCredential, nil
	}
	c.mtx.RUnlock()
	return c.initAzureCredential()
}

// GetAzureMySQLClient returns an AzureClient for MySQL for the given subscription.
func (c *cloudClients) GetAzureMySQLClient(subscription string) (azure.DBServersClient, error) {
	c.mtx.RLock()
	if client, ok := c.azureMySQLClients[subscription]; ok {
		c.mtx.RUnlock()
		return client, nil
	}
	c.mtx.RUnlock()
	return c.initAzureMySQLClient(subscription)
}

// GetAzurePostgresClient returns an AzureClient for Postgres for the given subscription.
func (c *cloudClients) GetAzurePostgresClient(subscription string) (azure.DBServersClient, error) {
	c.mtx.RLock()
	if client, ok := c.azurePostgresClients[subscription]; ok {
		c.mtx.RUnlock()
		return client, nil
	}
	c.mtx.RUnlock()
	return c.initAzurePostgresClient(subscription)
}

// GetAzureSubscriptionClient returns an Azure client for listing subscriptions.
func (c *cloudClients) GetAzureSubscriptionClient() (*azure.SubscriptionClient, error) {
	c.mtx.RLock()
	if c.azureSubscriptionsClient != nil {
		defer c.mtx.RUnlock()
		return c.azureSubscriptionsClient, nil
	}
	c.mtx.RUnlock()
	return c.initAzureSubscriptionsClient()
}

// GetAzureRedisClient returns an Azure Redis client for the given subscription.
func (c *cloudClients) GetAzureRedisClient(subscription string) (azure.RedisClient, error) {
	return c.azureRedisClients.Get(subscription, c.GetAzureCredential)
}

// GetAzureRedisEnterpriseClient returns an Azure Redis Enterprise client for the given subscription.
func (c *cloudClients) GetAzureRedisEnterpriseClient(subscription string) (azure.RedisEnterpriseClient, error) {
	return c.azureRedisEnterpriseClients.Get(subscription, c.GetAzureCredential)
}

// GetAzureSubscriptionClient returns an Azure client for listing AKS clusters.
func (c *cloudClients) GetAzureKubernetesClient(subscription string) (azure.AKSClient, error) {
	c.mtx.RLock()
	if client, ok := c.azureKubernetesClient[subscription]; ok {
		c.mtx.RUnlock()
		return client, nil
	}
	c.mtx.RUnlock()
	return c.initAzureKubernetesClient(subscription)
}

// GetAzureVirtualMachinesClient returns an Azure Virtual Machines client for
// the given subscription.
func (c *cloudClients) GetAzureVirtualMachinesClient(subscription string) (azure.VirtualMachinesClient, error) {
	return c.azureVirtualMachinesClients.Get(subscription, c.GetAzureCredential)
}

// Close closes all initialized clients.
func (c *cloudClients) Close() (err error) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.gcpIAM != nil {
		err = c.gcpIAM.Close()
		c.gcpIAM = nil
	}
	return trace.Wrap(err)
}

func (c *cloudClients) initAWSSession(region string) (*awssession.Session, error) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if session, ok := c.awsSessions[region]; ok { // If some other thead already got here first.
		return session, nil
	}
	logrus.Debugf("Initializing AWS session for region %v.", region)
	session, err := awssession.NewSessionWithOptions(awssession.Options{
		SharedConfigState: awssession.SharedConfigEnable,
		Config: aws.Config{
			Region: aws.String(region),
		},
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	c.awsSessions[region] = session
	return session, nil
}

func (c *cloudClients) initGCPIAMClient(ctx context.Context) (*gcpcredentials.IamCredentialsClient, error) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.gcpIAM != nil { // If some other thread already got here first.
		return c.gcpIAM, nil
	}
	logrus.Debug("Initializing GCP IAM client.")
	gcpIAM, err := gcpcredentials.NewIamCredentialsClient(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	c.gcpIAM = gcpIAM
	return gcpIAM, nil
}

func (c *cloudClients) initGCPSQLAdminClient(ctx context.Context) (gcp.SQLAdminClient, error) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.gcpSQLAdmin != nil { // If some other thread already got here first.
		return c.gcpSQLAdmin, nil
	}
	logrus.Debug("Initializing GCP Cloud SQL Admin client.")
	gcpSQLAdmin, err := gcp.NewSQLAdminClient(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	c.gcpSQLAdmin = gcpSQLAdmin
	return gcpSQLAdmin, nil
}

func (c *cloudClients) initGCPGKEClient(ctx context.Context) (gcp.GKEClient, error) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.gcpGKE != nil { // If some other thread already got here first.
		return c.gcpGKE, nil
	}
	logrus.Debug("Initializing GCP Cloud GKE client.")
	gcpGKE, err := gcp.NewGKEClient(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	c.gcpGKE = gcpGKE
	return gcpGKE, nil
}

func (c *cloudClients) initAzureCredential() (azcore.TokenCredential, error) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.azureCredential != nil { // If some other thread already got here first.
		return c.azureCredential, nil
	}
	logrus.Debug("Initializing Azure default credential chain.")
	// TODO(gavin): if/when we support AzureChina/AzureGovernment, we will need to specify the cloud in these options
	options := &azidentity.DefaultAzureCredentialOptions{}
	cred, err := azidentity.NewDefaultAzureCredential(options)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	c.azureCredential = cred
	return cred, nil
}

func (c *cloudClients) initAzureMySQLClient(subscription string) (azure.DBServersClient, error) {
	cred, err := c.GetAzureCredential()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()
	if client, ok := c.azureMySQLClients[subscription]; ok { // If some other thread already got here first.
		return client, nil
	}

	logrus.Debug("Initializing Azure MySQL servers client.")
	// TODO(gavin): if/when we support AzureChina/AzureGovernment, we will need to specify the cloud in these options
	options := &arm.ClientOptions{}
	api, err := armmysql.NewServersClient(subscription, cred, options)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	client := azure.NewMySQLServersClient(api)
	c.azureMySQLClients[subscription] = client
	return client, nil
}

func (c *cloudClients) initAzurePostgresClient(subscription string) (azure.DBServersClient, error) {
	cred, err := c.GetAzureCredential()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()
	if client, ok := c.azurePostgresClients[subscription]; ok { // If some other thread already got here first.
		return client, nil
	}
	logrus.Debug("Initializing Azure Postgres servers client.")
	// TODO(gavin): if/when we support AzureChina/AzureGovernment, we will need to specify the cloud in these options
	options := &arm.ClientOptions{}
	api, err := armpostgresql.NewServersClient(subscription, cred, options)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	client := azure.NewPostgresServerClient(api)
	c.azurePostgresClients[subscription] = client
	return client, nil
}

func (c *cloudClients) initAzureSubscriptionsClient() (*azure.SubscriptionClient, error) {
	cred, err := c.GetAzureCredential()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.azureSubscriptionsClient != nil { // If some other thread already got here first.
		return c.azureSubscriptionsClient, nil
	}
	logrus.Debug("Initializing Azure subscriptions client.")
	// TODO(gavin): if/when we support AzureChina/AzureGovernment,
	// we will need to specify the cloud in these options
	opts := &arm.ClientOptions{}
	armClient, err := armsubscription.NewSubscriptionsClient(cred, opts)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	client := azure.NewSubscriptionClient(armClient)
	c.azureSubscriptionsClient = client
	return client, nil
}

// initInstanceMetadata initializes the instance metadata client.
func (c *cloudClients) initInstanceMetadata(ctx context.Context) (InstanceMetadata, error) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.instanceMetadata != nil { // If some other thread already got here first.
		return c.instanceMetadata, nil
	}
	logrus.Debug("Initializing instance metadata client.")
	client, err := DiscoverInstanceMetadata(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	c.instanceMetadata = client
	return client, nil
}

func (c *cloudClients) initAzureKubernetesClient(subscription string) (azure.AKSClient, error) {
	cred, err := c.GetAzureCredential()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()
	if client, ok := c.azureKubernetesClient[subscription]; ok { // If some other thread already got here first.
		return client, nil
	}
	logrus.Debug("Initializing Azure AKS client.")
	// TODO(tigrato): if/when we support AzureChina/AzureGovernment, we will need to specify the cloud in these options
	options := &arm.ClientOptions{}
	api, err := armcontainerservice.NewManagedClustersClient(subscription, cred, options)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	client := azure.NewAKSClustersClient(
		api, func(options *azidentity.DefaultAzureCredentialOptions) (azure.GetToken, error) {
			cc, err := azidentity.NewDefaultAzureCredential(options)
			return cc, err
		})
	c.azureKubernetesClient[subscription] = client
	return client, nil
}

// TestCloudClients implements Clients
var _ Clients = (*TestCloudClients)(nil)

// TestCloudClients are used in tests.
type TestCloudClients struct {
	RDS                     rdsiface.RDSAPI
	RDSPerRegion            map[string]rdsiface.RDSAPI
	Redshift                redshiftiface.RedshiftAPI
	ElastiCache             elasticacheiface.ElastiCacheAPI
	MemoryDB                memorydbiface.MemoryDBAPI
	SecretsManager          secretsmanageriface.SecretsManagerAPI
	IAM                     iamiface.IAMAPI
	STS                     stsiface.STSAPI
	GCPSQL                  gcp.SQLAdminClient
	GCPGKE                  gcp.GKEClient
	EC2                     ec2iface.EC2API
	SSM                     ssmiface.SSMAPI
	InstanceMetadata        InstanceMetadata
	EKS                     eksiface.EKSAPI
	AzureMySQL              azure.DBServersClient
	AzureMySQLPerSub        map[string]azure.DBServersClient
	AzurePostgres           azure.DBServersClient
	AzurePostgresPerSub     map[string]azure.DBServersClient
	AzureSubscriptionClient *azure.SubscriptionClient
	AzureRedis              azure.RedisClient
	AzureRedisEnterprise    azure.RedisEnterpriseClient
	AzureAKSClientPerSub    map[string]azure.AKSClient
	AzureAKSClient          azure.AKSClient
	AzureVirtualMachines    azure.VirtualMachinesClient
}

// GetAWSSession returns AWS session for the specified region.
func (c *TestCloudClients) GetAWSSession(region string) (*awssession.Session, error) {
	return nil, trace.NotImplemented("not implemented")
}

// GetAWSRDSClient returns AWS RDS client for the specified region.
func (c *TestCloudClients) GetAWSRDSClient(region string) (rdsiface.RDSAPI, error) {
	if len(c.RDSPerRegion) != 0 {
		return c.RDSPerRegion[region], nil
	}
	return c.RDS, nil
}

// GetAWSRedshiftClient returns AWS Redshift client for the specified region.
func (c *TestCloudClients) GetAWSRedshiftClient(region string) (redshiftiface.RedshiftAPI, error) {
	return c.Redshift, nil
}

// GetAWSElastiCacheClient returns AWS ElastiCache client for the specified region.
func (c *TestCloudClients) GetAWSElastiCacheClient(region string) (elasticacheiface.ElastiCacheAPI, error) {
	return c.ElastiCache, nil
}

// GetAWSMemoryDBClient returns AWS MemoryDB client for the specified region.
func (c *TestCloudClients) GetAWSMemoryDBClient(region string) (memorydbiface.MemoryDBAPI, error) {
	return c.MemoryDB, nil
}

// GetAWSSecretsManagerClient returns AWS Secrets Manager client for the specified region.
func (c *TestCloudClients) GetAWSSecretsManagerClient(region string) (secretsmanageriface.SecretsManagerAPI, error) {
	return c.SecretsManager, nil
}

// GetAWSIAMClient returns AWS IAM client for the specified region.
func (c *TestCloudClients) GetAWSIAMClient(region string) (iamiface.IAMAPI, error) {
	return c.IAM, nil
}

// GetAWSSTSClient returns AWS STS client for the specified region.
func (c *TestCloudClients) GetAWSSTSClient(region string) (stsiface.STSAPI, error) {
	return c.STS, nil
}

// GetGCPIAMClient returns GCP IAM client.
func (c *TestCloudClients) GetGCPIAMClient(ctx context.Context) (*gcpcredentials.IamCredentialsClient, error) {
	return gcpcredentials.NewIamCredentialsClient(ctx,
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())), // Insecure must be set for unauth client.
		option.WithoutAuthentication())
}

// GetGCPSQLAdminClient returns GCP Cloud SQL Admin client.
func (c *TestCloudClients) GetGCPSQLAdminClient(ctx context.Context) (gcp.SQLAdminClient, error) {
	return c.GCPSQL, nil
}

// GetInstanceMetadata returns the instance metadata.
func (c *TestCloudClients) GetInstanceMetadataClient(ctx context.Context) (InstanceMetadata, error) {
	return c.InstanceMetadata, nil
}

// GetGCPGKEClient returns GKE client.
func (c *TestCloudClients) GetGCPGKEClient(ctx context.Context) (gcp.GKEClient, error) {
	return c.GCPGKE, nil
}

// GetAzureCredential returns default Azure token credential chain.
func (c *TestCloudClients) GetAzureCredential() (azcore.TokenCredential, error) {
	return &azidentity.ChainedTokenCredential{}, nil
}

// GetAWSEC2Client returns AWS EC2 client for the specified region.
func (c *TestCloudClients) GetAWSEC2Client(region string) (ec2iface.EC2API, error) {
	return c.EC2, nil
}

// GetAzureMySQLClient returns an AzureMySQLClient for the specified subscription
func (c *TestCloudClients) GetAzureMySQLClient(subscription string) (azure.DBServersClient, error) {
	if len(c.AzureMySQLPerSub) != 0 {
		return c.AzureMySQLPerSub[subscription], nil
	}
	return c.AzureMySQL, nil
}

// GetAWSEKSClient returns AWS EKS client for the specified region.
func (c *TestCloudClients) GetAWSEKSClient(region string) (eksiface.EKSAPI, error) {
	return c.EKS, nil
}

// GetAzurePostgresClient returns an AzurePostgresClient for the specified subscription
func (c *TestCloudClients) GetAzurePostgresClient(subscription string) (azure.DBServersClient, error) {
	if len(c.AzurePostgresPerSub) != 0 {
		return c.AzurePostgresPerSub[subscription], nil
	}
	return c.AzurePostgres, nil
}

// GetAzureKubernetesClient returns an AKS client for the specified subscription
func (c *TestCloudClients) GetAzureKubernetesClient(subscription string) (azure.AKSClient, error) {
	if len(c.AzurePostgresPerSub) != 0 {
		return c.AzureAKSClientPerSub[subscription], nil
	}
	return c.AzureAKSClient, nil
}

// GetAzureSubscriptionClient returns an Azure SubscriptionClient
func (c *TestCloudClients) GetAzureSubscriptionClient() (*azure.SubscriptionClient, error) {
	return c.AzureSubscriptionClient, nil
}

// GetAWSSSMClient returns an AWS SSM client
func (c *TestCloudClients) GetAWSSSMClient(region string) (ssmiface.SSMAPI, error) {
	return c.SSM, nil
}

// GetAzureRedisClient returns an Azure Redis client for the given subscription.
func (c *TestCloudClients) GetAzureRedisClient(subscription string) (azure.RedisClient, error) {
	return c.AzureRedis, nil
}

// GetAzureRedisEnterpriseClient returns an Azure Redis Enterprise client for the given subscription.
func (c *TestCloudClients) GetAzureRedisEnterpriseClient(subscription string) (azure.RedisEnterpriseClient, error) {
	return c.AzureRedisEnterprise, nil
}

// GetAzureVirtualMachinesClient returns an Azure Virtual Machines client for
// the given subscription.
func (c *TestCloudClients) GetAzureVirtualMachinesClient(subscription string) (azure.VirtualMachinesClient, error) {
	return c.AzureVirtualMachines, nil
}

// Close closes all initialized clients.
func (c *TestCloudClients) Close() error {
	return nil
}
