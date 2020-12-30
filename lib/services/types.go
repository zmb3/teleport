package services

import (
	"github.com/gravitational/teleport/api/constants"
	"github.com/gravitational/teleport/api/types"
)

// The following types are implemented in /api/types, and imported/wrapped here.
// The new structs are used to wrap the imported types with additional methods.
// The other types are basic imports and can be removed if their references are updated.

type AccessRequest = types.AccessRequest
type AccessRequestFilter = types.AccessRequestFilter
type AccessRequestConditions = types.AccessRequestConditions
type RequestState = types.RequestState

var NewAccessRequest = types.NewAccessRequest

type AuthPreference = types.AuthPreference

var NewAuthPreference = types.NewAuthPreference

type ClusterName = types.ClusterName

var NewClusterName = types.NewClusterName

type ClusterConfig = types.ClusterConfig

var NewClusterConfig = types.NewClusterConfig
var DefaultClusterConfig = types.DefaultClusterConfig

type App = types.App

type CertAuthority = types.CertAuthority
type CertAuthorityV1 = types.CertAuthorityV1

var NewCertAuthority = types.NewCertAuthority
var NewJWTAuthority = types.NewJWTAuthority

type Duration = types.Duration

type Event = types.Event

type ExternalIdentity = types.ExternalIdentity

type KubernetesCluster = types.KubernetesCluster

type License = types.License

var NewLicence = types.NewLicense

type OIDCConnector = types.OIDCConnector

var NewOIDCConnector = types.NewOIDCConnector

type SAMLConnector = types.SAMLConnector

var NewSAMLConnector = types.NewSAMLConnector

type GithubConnector = types.GithubConnector

var NewGithubConnector = types.NewGithubConnector

type MarshalConfig = types.MarshalConfig
type MarshalOption = types.MarshalOption

type PluginData = types.PluginData
type PluginDataFilter = types.PluginDataFilter
type PluginDataEntry = types.PluginDataEntry
type PluginDataUpdateParams = types.PluginDataUpdateParams

var NewPluginData = types.NewPluginData

type Presence = types.Presence
type ProxyGetter = types.ProxyGetter
type KeepAliver = types.KeepAliver

type Provisioner = types.Provisioner
type ProvisionToken = types.ProvisionToken

var NewProvisionToken = types.NewProvisionToken
var MustCreateProvisionToken = types.MustCreateProvisionToken

type RemoteCluster = types.RemoteCluster

var NewRemoteCluster = types.NewRemoteCluster

type ResetPasswordToken = types.ResetPasswordToken
type ResetPasswordTokenSecrets = types.ResetPasswordTokenSecrets

var NewResetPasswordToken = types.NewResetPasswordToken
var NewResetPasswordTokenSecrets = types.NewResetPasswordTokenSecrets

type Resource = types.Resource
type ResourceWithSecrets = types.ResourceWithSecrets
type ResourceHeader = types.ResourceHeader
type Metadata = types.Metadata

type ReverseTunnel = types.ReverseTunnel
type TunnelType = types.TunnelType

var NewReverseTunnel = types.NewReverseTunnel

type Role = types.Role
type RoleV3 = types.RoleV3
type RoleSpecV3 = types.RoleSpecV3
type RoleConditions = types.RoleConditions
type RoleConditionType = types.RoleConditionType
type RoleOptions = types.RoleOptions
type Rule = types.Rule
type Labels = types.Labels

var NewRule = types.NewRule
var NewBoolOption = types.NewBoolOption

type Rotation = types.Rotation

type RuleContext = types.RuleContext

type Semaphore = types.Semaphore

type Server = types.Server
type CommandLabel = types.CommandLabel

type StaticTokens = types.StaticTokens

var NewStaticTokens = types.NewStaticTokens
var DefaultStaticTokens = types.DefaultStaticTokens
var NewBool = types.NewBool

type Trust = types.Trust

type TraitMapping = types.TraitMapping
type TraitMappingSet = types.TraitMappingSet

type TrustedCluster = types.TrustedCluster
type RoleMap = types.RoleMap

var NewTrustedCluster = types.NewTrustedCluster

type TunnelConnection = types.TunnelConnection

type RoleMapping = types.RoleMapping

type User = types.User
type ConnectorRef = types.ConnectorRef

var NewUser = types.NewUser

type WebSession = types.WebSession

type ServerV2 = types.ServerV2
type RemoteClusterV3 = types.RemoteClusterV3

type Context = types.Context
type UserV2 = types.UserV2
type UserSpecV2 = types.UserSpecV2
type CommandLabelV2 = types.CommandLabelV2
type ServerSpecV2 = types.ServerSpecV2
type CertAuthorityV2 = types.CertAuthorityV2
type CertAuthoritySpecV2 = types.CertAuthoritySpecV2

var UserCA = types.UserCA
var GetCertAuthorityMarshaler = types.GetCertAuthorityMarshaler

// Some functions and variables  need to be imported from the types package
var (
	UnmarshalRole         = types.UnmarshalRole
	BoolDefaultTrue       = types.BoolDefaultTrue
	GetWhereParserFn      = types.GetWhereParserFn
	GetActionsParserFn    = types.GetActionsParserFn
	CombineLabels         = types.CombineLabels
	ProcessNamespace      = types.ProcessNamespace
	UnmarshalCertRoles    = types.UnmarshalCertRoles
	ParseSigningAlg       = types.ParseSigningAlg
	MaxDuration           = types.MaxDuration
	NewDuration           = types.NewDuration
	IsValidLabelKey       = types.IsValidLabelKey
	RequestState_NONE     = types.RequestState_NONE
	RequestState_PENDING  = types.RequestState_PENDING
	RequestState_APPROVED = types.RequestState_APPROVED
	RequestState_DENIED   = types.RequestState_DENIED
)

// The following constants are imported from api/types to simplify
// refactoring. These could be removed and their references updated.
const (
	NodeTunnel  = types.NodeTunnel
	ProxyTunnel = types.ProxyTunnel
	AppTunnel   = types.AppTunnel
	KubeTunnel  = types.KubeTunnel
)

// The following Constants are imported from api/constants to simplify
// refactoring. These could be removed and their references updated.
const (
	DefaultAPIGroup               = constants.DefaultAPIGroup
	ActionRead                    = constants.ActionRead
	ActionWrite                   = constants.ActionWrite
	Wildcard                      = constants.Wildcard
	KindNamespace                 = constants.KindNamespace
	KindUser                      = constants.KindUser
	KindKeyPair                   = constants.KindKeyPair
	KindHostCert                  = constants.KindHostCert
	KindJWT                       = constants.KindJWT
	KindLicense                   = constants.KindLicense
	KindRole                      = constants.KindRole
	KindAccessRequest             = constants.KindAccessRequest
	KindPluginData                = constants.KindPluginData
	KindOIDC                      = constants.KindOIDC
	KindSAML                      = constants.KindSAML
	KindGithub                    = constants.KindGithub
	KindOIDCRequest               = constants.KindOIDCRequest
	KindSAMLRequest               = constants.KindSAMLRequest
	KindGithubRequest             = constants.KindGithubRequest
	KindSession                   = constants.KindSession
	KindSSHSession                = constants.KindSSHSession
	KindWebSession                = constants.KindWebSession
	KindAppSession                = constants.KindAppSession
	KindEvent                     = constants.KindEvent
	KindAuthServer                = constants.KindAuthServer
	KindProxy                     = constants.KindProxy
	KindNode                      = constants.KindNode
	KindAppServer                 = constants.KindAppServer
	KindToken                     = constants.KindToken
	KindCertAuthority             = constants.KindCertAuthority
	KindReverseTunnel             = constants.KindReverseTunnel
	KindOIDCConnector             = constants.KindOIDCConnector
	KindSAMLConnector             = constants.KindSAMLConnector
	KindGithubConnector           = constants.KindGithubConnector
	KindConnectors                = constants.KindConnectors
	KindClusterAuthPreference     = constants.KindClusterAuthPreference
	MetaNameClusterAuthPreference = constants.MetaNameClusterAuthPreference
	KindClusterConfig             = constants.KindClusterConfig
	KindSemaphore                 = constants.KindSemaphore
	MetaNameClusterConfig         = constants.MetaNameClusterConfig
	KindClusterName               = constants.KindClusterName
	MetaNameClusterName           = constants.MetaNameClusterName
	KindStaticTokens              = constants.KindStaticTokens
	MetaNameStaticTokens          = constants.MetaNameStaticTokens
	KindTrustedCluster            = constants.KindTrustedCluster
	KindAuthConnector             = constants.KindAuthConnector
	KindTunnelConnection          = constants.KindTunnelConnection
	KindRemoteCluster             = constants.KindRemoteCluster
	KindResetPasswordToken        = constants.KindResetPasswordToken
	KindResetPasswordTokenSecrets = constants.KindResetPasswordTokenSecrets
	KindIdentity                  = constants.KindIdentity
	KindState                     = constants.KindState
	KindKubeService               = constants.KindKubeService
	V3                            = constants.V3
	V2                            = constants.V2
	V1                            = constants.V1
	VerbList                      = constants.VerbList
	VerbCreate                    = constants.VerbCreate
	VerbRead                      = constants.VerbRead
	VerbReadNoSecrets             = constants.VerbReadNoSecrets
	VerbUpdate                    = constants.VerbUpdate
	VerbDelete                    = constants.VerbDelete
	VerbRotate                    = constants.VerbRotate
)
