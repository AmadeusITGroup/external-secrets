/*
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
limitations under the License.
*/
package gcp

import (
	"context"
	"fmt"
	"os"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"

	// nolint
	. "github.com/onsi/ginkgo/v2"

	// nolint
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	. "github.com/onsi/gomega"
	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/jwt"
	"google.golang.org/api/option"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilpointer "k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/external-secrets/external-secrets-e2e/framework"
	esv1 "github.com/external-secrets/external-secrets/apis/externalsecrets/v1"
	esmeta "github.com/external-secrets/external-secrets/apis/meta/v1"
	gcpsm "github.com/external-secrets/external-secrets/pkg/provider/gcp/secretmanager"
)

// nolint // Better to keep names consistent even if it stutters;
type GcpProvider struct {
	ServiceAccountName      string
	ServiceAccountNamespace string

	framework       *framework.Framework
	credentials     string
	projectID       string
	clusterLocation string
	clusterName     string
	controllerClass string
}

func NewGCPProvider(f *framework.Framework, credentials, projectID string,
	clusterLocation string, clusterName string, serviceAccountName string, serviceAccountNamespace string, controllerClass string) *GcpProvider {
	prov := &GcpProvider{
		credentials:             credentials,
		projectID:               projectID,
		framework:               f,
		clusterLocation:         clusterLocation,
		clusterName:             clusterName,
		ServiceAccountName:      serviceAccountName,
		ServiceAccountNamespace: serviceAccountNamespace,
		controllerClass:         controllerClass,
	}

	BeforeEach(func() {
		prov.CreateSAKeyStore()
		prov.CreateReferentSAKeyStore()
		prov.CreateSpecifcSASecretStore()
		prov.CreatePodIDStore()
	})

	AfterEach(func() {
		prov.DeleteSpecifcSASecretStore()
	})

	return prov
}

func NewFromEnv(f *framework.Framework, controllerClass string) *GcpProvider {
	projectID := os.Getenv("GCP_PROJECT_ID")
	credentials := os.Getenv("GCP_SM_SA_JSON")
	serviceAccountName := os.Getenv("GCP_KSA_NAME")
	serviceAccountNamespace := "default"
	clusterLocation := os.Getenv("GCP_GKE_ZONE")
	clusterName := os.Getenv("GCP_GKE_CLUSTER")
	return NewGCPProvider(f, credentials, projectID, clusterLocation, clusterName, serviceAccountName, serviceAccountNamespace, controllerClass)
}

func (s *GcpProvider) getClient(ctx context.Context) (client *secretmanager.Client, err error) {
	var config *jwt.Config
	config, err = google.JWTConfigFromJSON([]byte(s.credentials), gcpsm.CloudPlatformRole)
	Expect(err).ToNot(HaveOccurred())
	ts := config.TokenSource(ctx)
	client, err = secretmanager.NewClient(ctx, option.WithTokenSource(ts))
	Expect(err).ToNot(HaveOccurred())
	return client, err
}

func (s *GcpProvider) CreateSecret(key string, val framework.SecretEntry) {
	ctx := context.Background()
	client, err := s.getClient(ctx)
	Expect(err).ToNot(HaveOccurred())
	defer client.Close()
	// Create the request to create the secret.
	createSecretReq := &secretmanagerpb.CreateSecretRequest{
		Parent:   fmt.Sprintf("projects/%s", s.projectID),
		SecretId: key,
		Secret: &secretmanagerpb.Secret{
			Labels: val.Tags,
			Replication: &secretmanagerpb.Replication{
				Replication: &secretmanagerpb.Replication_Automatic_{
					Automatic: &secretmanagerpb.Replication_Automatic{},
				},
			},
		},
	}
	secret, err := client.CreateSecret(ctx, createSecretReq)
	Expect(err).ToNot(HaveOccurred())
	addSecretVersionReq := &secretmanagerpb.AddSecretVersionRequest{
		Parent: secret.Name,
		Payload: &secretmanagerpb.SecretPayload{
			Data: []byte(val.Value),
		},
	}
	_, err = client.AddSecretVersion(ctx, addSecretVersionReq)
	Expect(err).ToNot(HaveOccurred())
}

func (s *GcpProvider) DeleteSecret(key string) {
	ctx := context.Background()
	client, err := s.getClient(ctx)
	Expect(err).ToNot(HaveOccurred())
	Expect(err).ToNot(HaveOccurred())
	defer client.Close()
	req := &secretmanagerpb.DeleteSecretRequest{
		Name: fmt.Sprintf("projects/%s/secrets/%s", s.projectID, key),
	}
	err = client.DeleteSecret(ctx, req)
	Expect(err).ToNot(HaveOccurred())
}

func makeStore(s *GcpProvider) *esv1.SecretStore {
	return &esv1.SecretStore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      s.framework.Namespace.Name,
			Namespace: s.framework.Namespace.Name,
		},
		Spec: esv1.SecretStoreSpec{
			Controller: s.controllerClass,
			Provider: &esv1.SecretStoreProvider{
				GCPSM: &esv1.GCPSMProvider{
					ProjectID: s.projectID,
				},
			},
		},
	}
}

const (
	serviceAccountKey           = "secret-access-credentials"
	PodIDSecretStoreName        = "pod-identity"
	staticCredentialsSecretName = "provider-secret"
)

func (s *GcpProvider) CreateSAKeyStore() {
	gcpCreds := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      staticCredentialsSecretName,
			Namespace: s.framework.Namespace.Name,
		},
		StringData: map[string]string{
			serviceAccountKey: s.credentials,
		},
	}
	err := s.framework.CRClient.Create(context.Background(), gcpCreds)
	if err != nil {
		err = s.framework.CRClient.Update(context.Background(), gcpCreds)
		Expect(err).ToNot(HaveOccurred())
	}
	secretStore := makeStore(s)
	secretStore.Spec.Provider.GCPSM.Auth = esv1.GCPSMAuth{
		SecretRef: &esv1.GCPSMAuthSecretRef{
			SecretAccessKey: esmeta.SecretKeySelector{
				Name: staticCredentialsSecretName,
				Key:  serviceAccountKey,
			},
		},
	}
	err = s.framework.CRClient.Create(context.Background(), secretStore)
	Expect(err).ToNot(HaveOccurred())
}

func (s *GcpProvider) CreateReferentSAKeyStore() {
	gcpCreds := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      referentName(s.framework),
			Namespace: s.framework.Namespace.Name,
		},
		StringData: map[string]string{
			serviceAccountKey: s.credentials,
		},
	}
	err := s.framework.CRClient.Create(context.Background(), gcpCreds)
	if err != nil {
		err = s.framework.CRClient.Update(context.Background(), gcpCreds)
		Expect(err).ToNot(HaveOccurred())
	}

	css := &esv1.ClusterSecretStore{
		ObjectMeta: metav1.ObjectMeta{
			Name: referentName(s.framework),
		},
		Spec: esv1.SecretStoreSpec{
			Controller: s.controllerClass,
			Provider: &esv1.SecretStoreProvider{
				GCPSM: &esv1.GCPSMProvider{
					ProjectID: s.projectID,
					Auth: esv1.GCPSMAuth{
						SecretRef: &esv1.GCPSMAuthSecretRef{
							SecretAccessKey: esmeta.SecretKeySelector{
								Name: referentName(s.framework),
								Key:  serviceAccountKey,
							},
						},
					},
				},
			},
		},
	}
	err = s.framework.CRClient.Create(context.Background(), css)
	Expect(err).ToNot(HaveOccurred())
}

func referentName(f *framework.Framework) string {
	return "referent-auth-" + f.Namespace.Name
}

func (s *GcpProvider) CreatePodIDStore() {
	secretStore := makeStore(s)
	secretStore.ObjectMeta.Name = PodIDSecretStoreName
	err := s.framework.CRClient.Create(context.Background(), secretStore)
	Expect(err).ToNot(HaveOccurred())
}

func (s *GcpProvider) SAClusterSecretStoreName() string {
	return "gcpsa-" + s.framework.Namespace.Name
}

func (s *GcpProvider) CreateSpecifcSASecretStore() {
	clusterSecretStore := &esv1.ClusterSecretStore{
		ObjectMeta: metav1.ObjectMeta{
			Name: s.SAClusterSecretStoreName(),
		},
	}
	_, err := controllerutil.CreateOrUpdate(context.Background(), s.framework.CRClient, clusterSecretStore, func() error {
		clusterSecretStore.Spec.Controller = s.controllerClass
		clusterSecretStore.Spec.Provider = &esv1.SecretStoreProvider{
			GCPSM: &esv1.GCPSMProvider{
				ProjectID: s.projectID,
				Auth: esv1.GCPSMAuth{
					WorkloadIdentity: &esv1.GCPWorkloadIdentity{
						ClusterLocation: s.clusterLocation,
						ClusterName:     s.clusterName,
						ServiceAccountRef: esmeta.ServiceAccountSelector{
							Name:      s.ServiceAccountName,
							Namespace: utilpointer.String(s.ServiceAccountNamespace),
						},
					},
				},
			},
		}
		return nil
	})
	Expect(err).ToNot(HaveOccurred())
}

// Cleanup removes global resources that may have been
// created by this provider.
func (s *GcpProvider) DeleteSpecifcSASecretStore() {
	err := s.framework.CRClient.Delete(context.Background(), &esv1.ClusterSecretStore{
		ObjectMeta: metav1.ObjectMeta{
			Name: s.SAClusterSecretStoreName(),
		},
	})
	Expect(err).ToNot(HaveOccurred())
}
