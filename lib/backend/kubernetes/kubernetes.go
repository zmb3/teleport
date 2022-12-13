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

package kubernetes

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/gravitational/trace"
	corev1 "k8s.io/api/core/v1"
	kubeerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	applyconfigv1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/zmb3/teleport/lib/backend"
	kubeutils "github.com/zmb3/teleport/lib/kube/utils"
)

const (
	secretIdentifierName   = "state"
	namespaceEnv           = "KUBE_NAMESPACE"
	teleportReplicaNameEnv = "TELEPORT_REPLICA_NAME"
	releaseNameEnv         = "RELEASE_NAME"
)

// InKubeCluster detemines if the agent is running inside a Kubernetes cluster and has access to
// service account token and cluster CA. Besides, it also validates the presence of `KUBE_NAMESPACE`
// and `TELEPORT_REPLICA_NAME` environment variables to generate the secret name.
func InKubeCluster() bool {
	_, _, err := kubeutils.GetKubeClient("")

	return err == nil &&
		len(os.Getenv(namespaceEnv)) > 0 &&
		len(os.Getenv(teleportReplicaNameEnv)) > 0
}

// Config structure represents configuration section
type Config struct {
	// Namespace is the Agent's namespace
	// Field is required
	Namespace string
	// SecretName is unique secret per agent where state and identity will be stored.
	// Field is required
	SecretName string
	// ReplicaName is the Agent's pod name
	// Field is required
	ReplicaName string
	// ReleaseName is the HELM release name
	// Field is optional
	ReleaseName string
	// KubeClient is the Kubernetes rest client
	// Field is required
	KubeClient kubernetes.Interface
}

func (c Config) Check() error {
	if len(c.Namespace) == 0 {
		return trace.BadParameter("missing namespace")
	}

	if len(c.SecretName) == 0 {
		return trace.BadParameter("missing secret name")
	}

	if len(c.ReplicaName) == 0 {
		return trace.BadParameter("missing replica name")
	}

	if c.KubeClient == nil {
		return trace.BadParameter("missing Kubernetes client")
	}

	return nil
}

// Backend uses Kubernetes Secrets to store identities.
type Backend struct {
	Config

	// Mutex is used to limit the number of concurrent operations per agent to 1 so we do not need
	// to handle retries locally.
	// The same happens with SQlite backend.
	mu sync.Mutex
}

// New returns a new instance of Kubernetes Secret identity backend storage.
func New() (*Backend, error) {
	restClient, _, err := kubeutils.GetKubeClient("")
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return NewWithClient(restClient)
}

// NewWithClient returns a new instance of Kubernetes Secret identity backend storage with the provided client.
func NewWithClient(restClient kubernetes.Interface) (*Backend, error) {
	for _, env := range []string{teleportReplicaNameEnv, namespaceEnv} {
		if len(os.Getenv(env)) == 0 {
			return nil, trace.BadParameter("environment variable %q not set or empty", env)
		}
	}

	return NewWithConfig(
		Config{
			Namespace: os.Getenv(namespaceEnv),
			SecretName: fmt.Sprintf(
				"%s-%s",
				os.Getenv(teleportReplicaNameEnv),
				secretIdentifierName,
			),
			ReplicaName: os.Getenv(teleportReplicaNameEnv),
			ReleaseName: os.Getenv(releaseNameEnv),
			KubeClient:  restClient,
		},
	)
}

// NewWithConfig returns a new instance of Kubernetes Secret identity backend storage with the provided config.
func NewWithConfig(conf Config) (*Backend, error) {
	if err := conf.Check(); err != nil {
		return nil, trace.Wrap(err)
	}

	return &Backend{
		Config: conf,
		mu:     sync.Mutex{},
	}, nil
}

// Exists checks if the secret already exists in Kubernetes.
// It's used to determine if the agent never created a secret and might upgrade from
// local SQLite database. In that case, the agent reads local database and
// creates a copy of the keys in Kube Secret.
func (b *Backend) Exists(ctx context.Context) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	_, err := b.getSecret(ctx)
	return err == nil
}

// Get reads the secret and extracts the key from it.
// If the secret does not exist or the key is not found it returns trace.Notfound,
// otherwise returns the underlying error.
func (b *Backend) Get(ctx context.Context, key []byte) (*backend.Item, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.readSecretData(ctx, key)
}

// Create creates item
func (b *Backend) Create(ctx context.Context, i backend.Item) (*backend.Lease, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.updateSecretContent(ctx, i)
}

// Put puts value into backend (creates if it does not exist, updates it otherwise)
func (b *Backend) Put(ctx context.Context, i backend.Item) (*backend.Lease, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.updateSecretContent(ctx, i)
}

// PutRange receives multiple items and upserts them into the Kubernetes Secret.
// This function is only used when the Agent's Secret does not exist, but local SQLite database
// has identity credentials.
// TODO(tigrato): remove this once the compatibility layer between local storage and
// Kube secret storage is no longer required!
func (b *Backend) PutRange(ctx context.Context, items []backend.Item) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	_, err := b.updateSecretContent(ctx, items...)
	return trace.Wrap(err)
}

// getSecret reads the secret from K8S API.
func (b *Backend) getSecret(ctx context.Context) (*corev1.Secret, error) {
	secret, err := b.KubeClient.
		CoreV1().
		Secrets(b.Namespace).
		Get(ctx, b.SecretName, metav1.GetOptions{})

	if kubeerrors.IsNotFound(err) {
		return nil, trace.NotFound("secret %v not found", b.SecretName)
	}

	return secret, trace.Wrap(err)
}

// readSecretData reads the secret content and extracts the content for key.
// returns an error if the key does not exist or the data is empty.
func (b *Backend) readSecretData(ctx context.Context, key []byte) (*backend.Item, error) {
	secret, err := b.getSecret(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	data, ok := secret.Data[backendKeyToSecret(key)]
	if !ok || len(data) == 0 {
		return nil, trace.NotFound("key [%s] not found in secret %s", string(key), b.SecretName)
	}

	return &backend.Item{
		Key:   key,
		Value: data,
	}, nil
}

func (b *Backend) updateSecretContent(ctx context.Context, items ...backend.Item) (*backend.Lease, error) {
	// FIXME(tigrato):
	// for now, the agent is the owner of the secret so it's safe to replace changes

	secret, err := b.getSecret(ctx)
	if trace.IsNotFound(err) {
		secret = b.genSecretObject()
	} else if err != nil {
		return nil, trace.Wrap(err)
	}

	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}

	updateDataMap(secret.Data, items...)

	if err := b.upsertSecret(ctx, secret); err != nil {
		return nil, trace.Wrap(err)
	}

	return &backend.Lease{}, nil
}

func (b *Backend) upsertSecret(ctx context.Context, secret *corev1.Secret) error {
	secretApply := applyconfigv1.Secret(b.SecretName, b.Namespace).
		WithData(secret.Data).
		WithLabels(secret.GetLabels()).
		WithAnnotations(secret.GetAnnotations())

	// apply resource lock if it's not a creation
	if len(secret.ResourceVersion) > 0 {
		secretApply = secretApply.WithResourceVersion(secret.ResourceVersion)
	}

	_, err := b.KubeClient.
		CoreV1().
		Secrets(b.Namespace).
		Apply(ctx, secretApply, metav1.ApplyOptions{FieldManager: b.ReplicaName})

	return trace.Wrap(err)
}

func (b *Backend) genSecretObject() *corev1.Secret {
	return &corev1.Secret{
		Type: corev1.SecretTypeOpaque,
		ObjectMeta: metav1.ObjectMeta{
			Name:        b.SecretName,
			Namespace:   b.Namespace,
			Annotations: generateSecretAnnotations(b.Namespace, b.ReleaseName),
		},
		Data: map[string][]byte{},
	}

}

func generateSecretAnnotations(namespace, releaseNameEnv string) map[string]string {
	const (
		helmReleaseNameAnnotation     = "meta.helm.sh/release-name"
		helmReleaseNamesaceAnnotation = "meta.helm.sh/release-namespace"
		helmResourcePolicy            = "helm.sh/resource-policy"
	)

	if len(releaseNameEnv) > 0 {
		return map[string]string{
			helmReleaseNameAnnotation:     releaseNameEnv,
			helmReleaseNamesaceAnnotation: namespace,
			helmResourcePolicy:            "keep",
		}
	}

	return map[string]string{}
}

// backendKeyToSecret replaces the "/" with "."
// "/" chars are not allowed in Kubernetes Secret keys.
func backendKeyToSecret(k []byte) string {
	return strings.ReplaceAll(string(k), "/", ".")
}

func updateDataMap(data map[string][]byte, items ...backend.Item) {
	for _, item := range items {
		data[backendKeyToSecret(item.Key)] = item.Value
	}
}
