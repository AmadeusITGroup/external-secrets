/*
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

package pushsecret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	esv1 "github.com/external-secrets/external-secrets/apis/externalsecrets/v1"
	"github.com/external-secrets/external-secrets/apis/externalsecrets/v1alpha1"
	genv1alpha1 "github.com/external-secrets/external-secrets/apis/generators/v1alpha1"
	ctest "github.com/external-secrets/external-secrets/pkg/controllers/commontest"
	"github.com/external-secrets/external-secrets/pkg/controllers/pushsecret/psmetrics"
	"github.com/external-secrets/external-secrets/pkg/provider/testing/fake"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	fakeProvider *fake.Client
	timeout      = time.Second * 10
	interval     = time.Millisecond * 250
)

type testCase struct {
	store           esv1.GenericStore
	managedStore1   esv1.GenericStore
	managedStore2   esv1.GenericStore
	unmanagedStore1 esv1.GenericStore
	unmanagedStore2 esv1.GenericStore
	pushsecret      *v1alpha1.PushSecret
	secret          *v1.Secret
	assert          func(pushsecret *v1alpha1.PushSecret, secret *v1.Secret) bool
}

func init() {
	fakeProvider = fake.New()
	esv1.ForceRegister(fakeProvider, &esv1.SecretStoreProvider{
		Fake: &esv1.FakeProvider{},
	}, esv1.MaintenanceStatusMaintained)
	psmetrics.SetUpMetrics()
}

func checkCondition(status v1alpha1.PushSecretStatus, cond v1alpha1.PushSecretStatusCondition) bool {
	fmt.Printf("status: %+v\ncond: %+v\n", status.Conditions, cond)
	for _, condition := range status.Conditions {
		if condition.Message == cond.Message &&
			condition.Reason == cond.Reason &&
			condition.Status == cond.Status &&
			condition.Type == cond.Type {
			return true
		}
	}
	return false
}

type testTweaks func(*testCase)

var _ = Describe("PushSecret controller", func() {
	const (
		PushSecretName  = "test-ps"
		PushSecretStore = "test-store"
		SecretName      = "test-secret"
	)

	var PushSecretNamespace string

	// if we are in debug and need to increase the timeout for testing, we can do so by using an env var
	if customTimeout := os.Getenv("TEST_CUSTOM_TIMEOUT_SEC"); customTimeout != "" {
		if t, err := strconv.Atoi(customTimeout); err == nil {
			timeout = time.Second * time.Duration(t)
		}
	}

	BeforeEach(func() {
		var err error
		PushSecretNamespace, err = ctest.CreateNamespace("test-ns", k8sClient)
		Expect(err).ToNot(HaveOccurred())
		fakeProvider.Reset()

		Expect(k8sClient.Create(context.Background(), &genv1alpha1.Fake{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Fake",
				APIVersion: "generators.external-secrets.io/v1alpha1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test",
				Namespace: PushSecretNamespace,
			},
			Spec: genv1alpha1.FakeSpec{
				Data: map[string]string{
					"key": "foo-bar-from-generator",
				},
			}})).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		k8sClient.Delete(context.Background(), &v1alpha1.PushSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      PushSecretName,
				Namespace: PushSecretNamespace,
			},
		})
		// give a time for reconciler to remove finalizers before removing SecretStores
		time.Sleep(2 * time.Second)
		k8sClient.Delete(context.Background(), &esv1.SecretStore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      PushSecretStore,
				Namespace: PushSecretNamespace,
			},
		})
		k8sClient.Delete(context.Background(), &esv1.ClusterSecretStore{
			ObjectMeta: metav1.ObjectMeta{
				Name: PushSecretStore,
			},
		})
		k8sClient.Delete(context.Background(), &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      SecretName,
				Namespace: PushSecretNamespace,
			},
		})
		Expect(k8sClient.Delete(context.Background(), &v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: PushSecretNamespace,
			},
		})).To(Succeed())
	})

	const (
		defaultKey          = "key"
		defaultVal          = "value"
		defaultPath         = "path/to/key"
		otherKey            = "other-key"
		otherVal            = "other-value"
		otherPath           = "path/to/other-key"
		newKey              = "new-key"
		newVal              = "new-value"
		storePrefixTemplate = "SecretStore/%v"
	)

	makeDefaultTestcase := func() *testCase {
		return &testCase{
			pushsecret: &v1alpha1.PushSecret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      PushSecretName,
					Namespace: PushSecretNamespace,
				},
				Spec: v1alpha1.PushSecretSpec{
					SecretStoreRefs: []v1alpha1.PushSecretStoreRef{
						{
							Name: PushSecretStore,
							Kind: "SecretStore",
						},
					},
					Selector: v1alpha1.PushSecretSelector{
						Secret: &v1alpha1.PushSecretSecret{
							Name: SecretName,
						},
					},
					Data: []v1alpha1.PushSecretData{
						{
							Match: v1alpha1.PushSecretMatch{
								SecretKey: defaultKey,
								RemoteRef: v1alpha1.PushSecretRemoteRef{
									RemoteKey: defaultPath,
								},
							},
						},
					},
				},
			},
			secret: &v1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      SecretName,
					Namespace: PushSecretNamespace,
				},
				Data: map[string][]byte{
					defaultKey: []byte(defaultVal),
				},
			},
			store: &esv1.SecretStore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      PushSecretStore,
					Namespace: PushSecretNamespace,
				},
				TypeMeta: metav1.TypeMeta{
					Kind: "SecretStore",
				},
				Spec: esv1.SecretStoreSpec{
					Provider: &esv1.SecretStoreProvider{
						Fake: &esv1.FakeProvider{
							Data: []esv1.FakeProviderData{},
						},
					},
				},
			},
		}
	}

	// if target Secret name is not specified it should use the ExternalSecret name.
	syncSuccessfully := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			Eventually(func() bool {
				By("checking if Provider value got updated")
				secretValue := secret.Data[defaultKey]
				providerValue, ok := fakeProvider.SetSecretArgs[ps.Spec.Data[0].Match.RemoteRef.RemoteKey]
				if !ok {
					return false
				}
				got := providerValue.Value
				return bytes.Equal(got, secretValue)
			}, time.Second*10, time.Second).Should(BeTrue())
			return true
		}
	}

	updateIfNotExists := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		fakeProvider.SecretExistsFn = func(ctx context.Context, ref esv1.PushSecretRemoteRef) (bool, error) {
			_, ok := fakeProvider.SetSecretArgs[ref.GetRemoteKey()]
			return ok, nil
		}
		tc.pushsecret.Spec.UpdatePolicy = v1alpha1.PushSecretUpdatePolicyIfNotExists
		initialValue := fakeProvider.SetSecretArgs[tc.pushsecret.Spec.Data[0].Match.RemoteRef.RemoteKey].Value
		tc.secret.Data[defaultKey] = []byte(newVal)

		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			Eventually(func() bool {
				By("checking if Provider value did not get updated")
				Expect(k8sClient.Update(context.Background(), secret, &client.UpdateOptions{})).Should(Succeed())
				providerValue, ok := fakeProvider.SetSecretArgs[ps.Spec.Data[0].Match.RemoteRef.RemoteKey]
				if !ok {
					return false
				}
				got := providerValue.Value
				return bytes.Equal(got, initialValue)
			}, time.Second*10, time.Second).Should(BeTrue())
			return true
		}
	}

	updateIfNotExistsPartialSecrets := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		fakeProvider.SecretExistsFn = func(ctx context.Context, ref esv1.PushSecretRemoteRef) (bool, error) {
			_, ok := fakeProvider.SetSecretArgs[ref.GetRemoteKey()]
			return ok, nil
		}
		tc.pushsecret.Spec.UpdatePolicy = v1alpha1.PushSecretUpdatePolicyIfNotExists
		tc.pushsecret.Spec.Data = append(tc.pushsecret.Spec.Data, v1alpha1.PushSecretData{
			Match: v1alpha1.PushSecretMatch{
				SecretKey: otherKey,
				RemoteRef: v1alpha1.PushSecretRemoteRef{
					RemoteKey: otherPath,
				},
			},
		})

		initialValue := fakeProvider.SetSecretArgs[tc.pushsecret.Spec.Data[0].Match.RemoteRef.RemoteKey].Value
		tc.secret.Data[defaultKey] = []byte(newVal) // change initial value in secret
		tc.secret.Data[otherKey] = []byte(otherVal)

		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			Eventually(func() bool {
				By("checking if only not existing Provider value got updated")
				Expect(k8sClient.Update(context.Background(), secret, &client.UpdateOptions{})).Should(Succeed())
				providerValue, ok := fakeProvider.SetSecretArgs[ps.Spec.Data[0].Match.RemoteRef.RemoteKey]
				if !ok {
					return false
				}
				got := providerValue.Value
				otherProviderValue, ok := fakeProvider.SetSecretArgs[ps.Spec.Data[1].Match.RemoteRef.RemoteKey]
				if !ok {
					return false
				}
				gotOther := otherProviderValue.Value

				return bytes.Equal(gotOther, tc.secret.Data[otherKey]) && bytes.Equal(got, initialValue)
			}, time.Second*10, time.Second).Should(BeTrue())
			return true
		}
	}

	updateIfNotExistsSyncStatus := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		fakeProvider.SecretExistsFn = func(ctx context.Context, ref esv1.PushSecretRemoteRef) (bool, error) {
			_, ok := fakeProvider.SetSecretArgs[ref.GetRemoteKey()]
			return ok, nil
		}
		tc.pushsecret.Spec.UpdatePolicy = v1alpha1.PushSecretUpdatePolicyIfNotExists
		tc.pushsecret.Spec.Data = append(tc.pushsecret.Spec.Data, v1alpha1.PushSecretData{
			Match: v1alpha1.PushSecretMatch{
				SecretKey: otherKey,
				RemoteRef: v1alpha1.PushSecretRemoteRef{
					RemoteKey: otherPath,
				},
			},
		})
		tc.secret.Data[defaultKey] = []byte(newVal)
		tc.secret.Data[otherKey] = []byte(otherVal)
		updatedPS := &v1alpha1.PushSecret{}

		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			Eventually(func() bool {
				By("checking if PushSecret status gets updated correctly with UpdatePolicy=IfNotExists")
				Expect(k8sClient.Update(context.Background(), secret, &client.UpdateOptions{})).Should(Succeed())
				psKey := types.NamespacedName{Name: PushSecretName, Namespace: PushSecretNamespace}
				err := k8sClient.Get(context.Background(), psKey, updatedPS)
				if err != nil {
					return false
				}
				_, ok := updatedPS.Status.SyncedPushSecrets[fmt.Sprintf(storePrefixTemplate, PushSecretStore)][defaultPath]
				if !ok {
					return false
				}
				_, ok = updatedPS.Status.SyncedPushSecrets[fmt.Sprintf(storePrefixTemplate, PushSecretStore)][otherPath]
				if !ok {
					return false
				}
				expected := v1alpha1.PushSecretStatusCondition{
					Type:    v1alpha1.PushSecretReady,
					Status:  v1.ConditionTrue,
					Reason:  v1alpha1.ReasonSynced,
					Message: "PushSecret synced successfully. Existing secrets in providers unchanged.",
				}
				return checkCondition(ps.Status, expected)
			}, time.Second*10, time.Second).Should(BeTrue())
			return true
		}
	}

	updateIfNotExistsSyncFailed := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		fakeProvider.SecretExistsFn = func(ctx context.Context, ref esv1.PushSecretRemoteRef) (bool, error) {
			return false, errors.New("don't know")
		}
		tc.pushsecret.Spec.UpdatePolicy = v1alpha1.PushSecretUpdatePolicyIfNotExists
		initialValue := fakeProvider.SetSecretArgs[tc.pushsecret.Spec.Data[0].Match.RemoteRef.RemoteKey].Value
		tc.secret.Data[defaultKey] = []byte(newVal)

		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			Eventually(func() bool {
				By("checking if sync failed if secret existence cannot be verified in Provider")
				providerValue, ok := fakeProvider.SetSecretArgs[ps.Spec.Data[0].Match.RemoteRef.RemoteKey]
				if !ok {
					return false
				}
				got := providerValue.Value
				expected := v1alpha1.PushSecretStatusCondition{
					Type:    v1alpha1.PushSecretReady,
					Status:  v1.ConditionFalse,
					Reason:  v1alpha1.ReasonErrored,
					Message: "set secret failed: could not verify if secret exists in store: don't know",
				}
				return checkCondition(ps.Status, expected) && bytes.Equal(got, initialValue)
			}, time.Second*10, time.Second).Should(BeTrue())
			return true
		}
	}
	syncSuccessfullyReusingKeys := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		tc.pushsecret = &v1alpha1.PushSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      PushSecretName,
				Namespace: PushSecretNamespace,
			},
			Spec: v1alpha1.PushSecretSpec{
				SecretStoreRefs: []v1alpha1.PushSecretStoreRef{
					{
						Name: PushSecretStore,
						Kind: "SecretStore",
					},
				},
				Selector: v1alpha1.PushSecretSelector{
					Secret: &v1alpha1.PushSecretSecret{
						Name: SecretName,
					},
				},
				Data: []v1alpha1.PushSecretData{
					{
						Match: v1alpha1.PushSecretMatch{
							SecretKey: "otherKey",
							RemoteRef: v1alpha1.PushSecretRemoteRef{
								RemoteKey: defaultPath,
							},
						},
					},
				},
				Template: &esv1.ExternalSecretTemplate{
					Metadata: esv1.ExternalSecretTemplateMetadata{
						Labels: map[string]string{
							"foos": "ball",
						},
						Annotations: map[string]string{
							"hihi": "ga",
						},
					},
					Type:          v1.SecretTypeOpaque,
					EngineVersion: esv1.TemplateEngineV2,
					Data: map[string]string{
						defaultKey: "{{ .key | toString | upper }} was templated",
						"otherKey": "{{ .key | toString | upper }} was also templated",
					},
				},
			},
		}
		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			Eventually(func() bool {
				By("checking if Provider value got updated")
				providerValue, ok := fakeProvider.SetSecretArgs[ps.Spec.Data[0].Match.RemoteRef.RemoteKey]
				if !ok {
					return false
				}
				got := providerValue.Value
				return bytes.Equal(got, []byte("VALUE was also templated"))
			}, time.Second*10, time.Second).Should(BeTrue())
			return true
		}
	}
	// if target Secret name is not specified it should use the ExternalSecret name.
	syncSuccessfullyWithTemplate := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		tc.pushsecret = &v1alpha1.PushSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      PushSecretName,
				Namespace: PushSecretNamespace,
			},
			Spec: v1alpha1.PushSecretSpec{
				SecretStoreRefs: []v1alpha1.PushSecretStoreRef{
					{
						Name: PushSecretStore,
						Kind: "SecretStore",
					},
				},
				Selector: v1alpha1.PushSecretSelector{
					Secret: &v1alpha1.PushSecretSecret{
						Name: SecretName,
					},
				},
				Data: []v1alpha1.PushSecretData{
					{
						Match: v1alpha1.PushSecretMatch{
							SecretKey: defaultKey,
							RemoteRef: v1alpha1.PushSecretRemoteRef{
								RemoteKey: defaultPath,
							},
						},
					},
				},
				Template: &esv1.ExternalSecretTemplate{
					Metadata: esv1.ExternalSecretTemplateMetadata{
						Labels: map[string]string{
							"foos": "ball",
						},
						Annotations: map[string]string{
							"hihi": "ga",
						},
					},
					Type:          v1.SecretTypeOpaque,
					EngineVersion: esv1.TemplateEngineV2,
					Data: map[string]string{
						defaultKey: "{{ .key | toString | upper }} was templated",
					},
				},
			},
		}
		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			Eventually(func() bool {
				By("checking if Provider value got updated")
				providerValue, ok := fakeProvider.SetSecretArgs[ps.Spec.Data[0].Match.RemoteRef.RemoteKey]
				if !ok {
					return false
				}
				got := providerValue.Value
				return bytes.Equal(got, []byte("VALUE was templated"))
			}, time.Second*10, time.Second).Should(BeTrue())
			return true
		}
	}

	// if target Secret name is not specified it should use the ExternalSecret name.
	syncAndDeleteSuccessfully := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		tc.pushsecret = &v1alpha1.PushSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      PushSecretName,
				Namespace: PushSecretNamespace,
			},
			Spec: v1alpha1.PushSecretSpec{
				DeletionPolicy: v1alpha1.PushSecretDeletionPolicyDelete,
				SecretStoreRefs: []v1alpha1.PushSecretStoreRef{
					{
						Name: PushSecretStore,
						Kind: "SecretStore",
					},
				},
				Selector: v1alpha1.PushSecretSelector{
					Secret: &v1alpha1.PushSecretSecret{
						Name: SecretName,
					},
				},
				Data: []v1alpha1.PushSecretData{
					{
						Match: v1alpha1.PushSecretMatch{
							SecretKey: defaultKey,
							RemoteRef: v1alpha1.PushSecretRemoteRef{
								RemoteKey: defaultPath,
							},
						},
					},
				},
			},
		}
		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			ps.Spec.Data[0].Match.RemoteRef.RemoteKey = newKey
			updatedPS := &v1alpha1.PushSecret{}
			Expect(k8sClient.Update(context.Background(), ps, &client.UpdateOptions{})).Should(Succeed())
			Eventually(func() bool {
				psKey := types.NamespacedName{Name: PushSecretName, Namespace: PushSecretNamespace}
				By("checking if Provider value got updated")
				err := k8sClient.Get(context.Background(), psKey, updatedPS)
				if err != nil {
					return false
				}
				key, ok := updatedPS.Status.SyncedPushSecrets[fmt.Sprintf(storePrefixTemplate, PushSecretStore)][newKey]
				if !ok {
					return false
				}
				return key.Match.SecretKey == defaultKey
			}, time.Second*10, time.Second).Should(BeTrue())
			return true
		}
	}

	// if PushSecret's DeletionPolicy is cleared, it should delete successfully
	syncChangePolicyAndDeleteSuccessfully := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		tc.pushsecret = &v1alpha1.PushSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      PushSecretName,
				Namespace: PushSecretNamespace,
			},
			Spec: v1alpha1.PushSecretSpec{
				DeletionPolicy: v1alpha1.PushSecretDeletionPolicyDelete,
				SecretStoreRefs: []v1alpha1.PushSecretStoreRef{
					{
						Name: PushSecretStore,
						Kind: "SecretStore",
					},
				},
				Selector: v1alpha1.PushSecretSelector{
					Secret: &v1alpha1.PushSecretSecret{
						Name: SecretName,
					},
				},
				Data: []v1alpha1.PushSecretData{
					{
						Match: v1alpha1.PushSecretMatch{
							SecretKey: defaultKey,
							RemoteRef: v1alpha1.PushSecretRemoteRef{
								RemoteKey: defaultPath,
							},
						},
					},
				},
			},
		}
		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			ps.Spec.DeletionPolicy = v1alpha1.PushSecretDeletionPolicyNone
			updatedPS := &v1alpha1.PushSecret{}
			Expect(k8sClient.Update(context.Background(), ps, &client.UpdateOptions{})).Should(Succeed())
			Expect(k8sClient.Delete(context.Background(), ps, &client.DeleteOptions{})).Should(Succeed())
			Eventually(func() bool {
				psKey := types.NamespacedName{Name: PushSecretName, Namespace: PushSecretNamespace}
				By("checking if Get PushSecret returns not found")
				err := k8sClient.Get(context.Background(), psKey, updatedPS)
				if err != nil && client.IgnoreNotFound(err) == nil {
					return true
				}
				return false
			}, time.Second*10, time.Second).Should(BeTrue())
			return true
		}
	}

	failDelete := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		fakeProvider.DeleteSecretFn = func() error {
			return errors.New("Nope")
		}
		tc.pushsecret = &v1alpha1.PushSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      PushSecretName,
				Namespace: PushSecretNamespace,
			},
			Spec: v1alpha1.PushSecretSpec{
				DeletionPolicy: v1alpha1.PushSecretDeletionPolicyDelete,
				SecretStoreRefs: []v1alpha1.PushSecretStoreRef{
					{
						Name: PushSecretStore,
						Kind: "SecretStore",
					},
				},
				Selector: v1alpha1.PushSecretSelector{
					Secret: &v1alpha1.PushSecretSecret{
						Name: SecretName,
					},
				},
				Data: []v1alpha1.PushSecretData{
					{
						Match: v1alpha1.PushSecretMatch{
							SecretKey: defaultKey,
							RemoteRef: v1alpha1.PushSecretRemoteRef{
								RemoteKey: defaultPath,
							},
						},
					},
				},
			},
		}
		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			ps.Spec.Data[0].Match.RemoteRef.RemoteKey = newKey
			updatedPS := &v1alpha1.PushSecret{}
			Expect(k8sClient.Update(context.Background(), ps, &client.UpdateOptions{})).Should(Succeed())
			Eventually(func() bool {
				psKey := types.NamespacedName{Name: PushSecretName, Namespace: PushSecretNamespace}
				By("checking if synced secrets correspond to both keys")
				err := k8sClient.Get(context.Background(), psKey, updatedPS)
				if err != nil {
					return false
				}
				_, ok := updatedPS.Status.SyncedPushSecrets[fmt.Sprintf(storePrefixTemplate, PushSecretStore)][newKey]
				if !ok {
					return false
				}
				_, ok = updatedPS.Status.SyncedPushSecrets[fmt.Sprintf(storePrefixTemplate, PushSecretStore)][defaultPath]
				return ok
			}, time.Second*10, time.Second).Should(BeTrue())
			return true
		}
	}
	failDeleteStore := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		fakeProvider.DeleteSecretFn = func() error {
			return errors.New("boom")
		}
		tc.pushsecret.Spec.DeletionPolicy = v1alpha1.PushSecretDeletionPolicyDelete
		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			secondStore := &esv1.SecretStore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "new-store",
					Namespace: PushSecretNamespace,
				},
				TypeMeta: metav1.TypeMeta{
					Kind: "SecretStore",
				},
				Spec: esv1.SecretStoreSpec{
					Provider: &esv1.SecretStoreProvider{
						Fake: &esv1.FakeProvider{
							Data: []esv1.FakeProviderData{},
						},
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), secondStore, &client.CreateOptions{})).Should(Succeed())
			ps.Spec.SecretStoreRefs[0].Name = "new-store"
			updatedPS := &v1alpha1.PushSecret{}
			Expect(k8sClient.Update(context.Background(), ps, &client.UpdateOptions{})).Should(Succeed())
			Eventually(func() bool {
				psKey := types.NamespacedName{Name: PushSecretName, Namespace: PushSecretNamespace}
				By("checking if Provider value got updated")
				err := k8sClient.Get(context.Background(), psKey, updatedPS)
				if err != nil {
					return false
				}
				syncedLen := len(updatedPS.Status.SyncedPushSecrets)
				return syncedLen == 2
			}, time.Second*10, time.Second).Should(BeTrue())
			return true
		}
	}
	deleteWholeStore := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		fakeProvider.DeleteSecretFn = func() error {
			return nil
		}
		tc.pushsecret.Spec.DeletionPolicy = v1alpha1.PushSecretDeletionPolicyDelete
		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			secondStore := &esv1.SecretStore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "new-store",
					Namespace: PushSecretNamespace,
				},
				TypeMeta: metav1.TypeMeta{
					Kind: "SecretStore",
				},
				Spec: esv1.SecretStoreSpec{
					Provider: &esv1.SecretStoreProvider{
						Fake: &esv1.FakeProvider{
							Data: []esv1.FakeProviderData{},
						},
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), secondStore, &client.CreateOptions{})).Should(Succeed())
			ps.Spec.SecretStoreRefs[0].Name = "new-store"
			updatedPS := &v1alpha1.PushSecret{}
			Expect(k8sClient.Update(context.Background(), ps, &client.UpdateOptions{})).Should(Succeed())
			Eventually(func() bool {
				psKey := types.NamespacedName{Name: PushSecretName, Namespace: PushSecretNamespace}
				By("checking if Provider value got updated")
				err := k8sClient.Get(context.Background(), psKey, updatedPS)
				if err != nil {
					return false
				}
				key, ok := updatedPS.Status.SyncedPushSecrets["SecretStore/new-store"][defaultPath]
				if !ok {
					return false
				}
				syncedLen := len(updatedPS.Status.SyncedPushSecrets)
				if syncedLen != 1 {
					return false
				}
				return key.Match.SecretKey == defaultKey
			}, time.Second*10, time.Second).Should(BeTrue())
			return true
		}
	}
	// if conversion strategy is defined, revert the keys based on the strategy.
	syncSuccessfullyWithConversionStrategy := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		tc.pushsecret = &v1alpha1.PushSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      PushSecretName,
				Namespace: PushSecretNamespace,
			},
			Spec: v1alpha1.PushSecretSpec{
				SecretStoreRefs: []v1alpha1.PushSecretStoreRef{
					{
						Name: PushSecretStore,
						Kind: "SecretStore",
					},
				},
				Selector: v1alpha1.PushSecretSelector{
					Secret: &v1alpha1.PushSecretSecret{
						Name: SecretName,
					},
				},
				Data: []v1alpha1.PushSecretData{
					{
						ConversionStrategy: v1alpha1.PushSecretConversionReverseUnicode,
						Match: v1alpha1.PushSecretMatch{
							SecretKey: "some-array[0].entity",
							RemoteRef: v1alpha1.PushSecretRemoteRef{
								RemoteKey: defaultPath,
							},
						},
					},
				},
			},
		}
		tc.secret = &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      SecretName,
				Namespace: PushSecretNamespace,
			},
			Data: map[string][]byte{
				"some-array_U005b_0_U005d_.entity": []byte("value"),
			},
		}
		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			Eventually(func() bool {
				By("checking if Provider value got updated")
				secretValue := secret.Data["some-array_U005b_0_U005d_.entity"]
				providerValue, ok := fakeProvider.SetSecretArgs[ps.Spec.Data[0].Match.RemoteRef.RemoteKey]
				if !ok {
					return false
				}
				got := providerValue.Value
				return bytes.Equal(got, secretValue)
			}, time.Second*10, time.Second).Should(BeTrue())
			return true
		}
	}
	// if target Secret name is not specified it should use the ExternalSecret name.
	syncMatchingLabels := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		fakeProvider.DeleteSecretFn = func() error {
			return nil
		}
		tc.pushsecret = &v1alpha1.PushSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      PushSecretName,
				Namespace: PushSecretNamespace,
			},
			Spec: v1alpha1.PushSecretSpec{
				SecretStoreRefs: []v1alpha1.PushSecretStoreRef{
					{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"foo": "bar",
							},
						},
						Kind: "SecretStore",
						Name: PushSecretStore,
					},
				},
				Selector: v1alpha1.PushSecretSelector{
					Secret: &v1alpha1.PushSecretSecret{
						Name: SecretName,
					},
				},
				Data: []v1alpha1.PushSecretData{
					{
						Match: v1alpha1.PushSecretMatch{
							SecretKey: defaultKey,
							RemoteRef: v1alpha1.PushSecretRemoteRef{
								RemoteKey: defaultPath,
							},
						},
					},
				},
			},
		}
		tc.store = &esv1.SecretStore{
			TypeMeta: metav1.TypeMeta{
				Kind: "SecretStore",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      PushSecretStore,
				Namespace: PushSecretNamespace,
				Labels: map[string]string{
					"foo": "bar",
				},
			},
			Spec: esv1.SecretStoreSpec{
				Provider: &esv1.SecretStoreProvider{
					Fake: &esv1.FakeProvider{
						Data: []esv1.FakeProviderData{},
					},
				},
			},
		}
		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			secretValue := secret.Data[defaultKey]
			providerValue := fakeProvider.SetSecretArgs[ps.Spec.Data[0].Match.RemoteRef.RemoteKey].Value
			expected := v1alpha1.PushSecretStatusCondition{
				Type:    v1alpha1.PushSecretReady,
				Status:  v1.ConditionTrue,
				Reason:  v1alpha1.ReasonSynced,
				Message: "PushSecret synced successfully",
			}
			return bytes.Equal(secretValue, providerValue) && checkCondition(ps.Status, expected)
		}
	}
	syncWithClusterStore := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		tc.store = &esv1.ClusterSecretStore{
			TypeMeta: metav1.TypeMeta{
				Kind: "ClusterSecretStore",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: PushSecretStore,
			},
			Spec: esv1.SecretStoreSpec{
				Provider: &esv1.SecretStoreProvider{
					Fake: &esv1.FakeProvider{
						Data: []esv1.FakeProviderData{},
					},
				},
			},
		}
		tc.pushsecret.Spec.SecretStoreRefs[0].Kind = "ClusterSecretStore"
		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			secretValue := secret.Data[defaultKey]
			providerValue := fakeProvider.SetSecretArgs[ps.Spec.Data[0].Match.RemoteRef.RemoteKey].Value
			expected := v1alpha1.PushSecretStatusCondition{
				Type:    v1alpha1.PushSecretReady,
				Status:  v1.ConditionTrue,
				Reason:  v1alpha1.ReasonSynced,
				Message: "PushSecret synced successfully",
			}
			return bytes.Equal(secretValue, providerValue) && checkCondition(ps.Status, expected)
		}
	}

	syncWithGenerator := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		tc.pushsecret.Spec.Selector.Secret = nil
		tc.pushsecret.Spec.Selector.GeneratorRef = &esv1.GeneratorRef{
			APIVersion: "generators.external-secrets.io/v1alpha1",
			Kind:       "Fake",
			Name:       "test",
		}
		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			providerValue := fakeProvider.SetSecretArgs[ps.Spec.Data[0].Match.RemoteRef.RemoteKey].Value
			expected := v1alpha1.PushSecretStatusCondition{
				Type:    v1alpha1.PushSecretReady,
				Status:  v1.ConditionTrue,
				Reason:  v1alpha1.ReasonSynced,
				Message: "PushSecret synced successfully",
			}
			return bytes.Equal([]byte("foo-bar-from-generator"), providerValue) && checkCondition(ps.Status, expected)
		}
	}
	// if target Secret name is not specified it should use the ExternalSecret name.
	syncWithClusterStoreMatchingLabels := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		tc.pushsecret = &v1alpha1.PushSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      PushSecretName,
				Namespace: PushSecretNamespace,
			},
			Spec: v1alpha1.PushSecretSpec{
				SecretStoreRefs: []v1alpha1.PushSecretStoreRef{
					{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"foo": "bar",
							},
						},
						Kind: "ClusterSecretStore",
						Name: PushSecretStore,
					},
				},
				Selector: v1alpha1.PushSecretSelector{
					Secret: &v1alpha1.PushSecretSecret{
						Name: SecretName,
					},
				},
				Data: []v1alpha1.PushSecretData{
					{
						Match: v1alpha1.PushSecretMatch{
							SecretKey: defaultKey,
							RemoteRef: v1alpha1.PushSecretRemoteRef{
								RemoteKey: defaultPath,
							},
						},
					},
				},
			},
		}
		tc.store = &esv1.ClusterSecretStore{
			ObjectMeta: metav1.ObjectMeta{
				Name: PushSecretStore,
				Labels: map[string]string{
					"foo": "bar",
				},
			},
			Spec: esv1.SecretStoreSpec{
				Provider: &esv1.SecretStoreProvider{
					Fake: &esv1.FakeProvider{
						Data: []esv1.FakeProviderData{},
					},
				},
			},
		}
		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			secretValue := secret.Data[defaultKey]
			providerValue := fakeProvider.SetSecretArgs[ps.Spec.Data[0].Match.RemoteRef.RemoteKey].Value
			expected := v1alpha1.PushSecretStatusCondition{
				Type:    v1alpha1.PushSecretReady,
				Status:  v1.ConditionTrue,
				Reason:  v1alpha1.ReasonSynced,
				Message: "PushSecret synced successfully",
			}
			return bytes.Equal(secretValue, providerValue) && checkCondition(ps.Status, expected)
		}
	}
	// if target Secret name is not specified it should use the ExternalSecret name.
	failNoSecret := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		tc.secret = nil
		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			expected := v1alpha1.PushSecretStatusCondition{
				Type:    v1alpha1.PushSecretReady,
				Status:  v1.ConditionFalse,
				Reason:  v1alpha1.ReasonErrored,
				Message: "could not get source secret",
			}
			return checkCondition(ps.Status, expected)
		}
	}
	// if target Secret name is not specified it should use the ExternalSecret name.
	failNoSecretKey := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		tc.pushsecret.Spec.Data[0].Match.SecretKey = "unexisting"
		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			expected := v1alpha1.PushSecretStatusCondition{
				Type:    v1alpha1.PushSecretReady,
				Status:  v1.ConditionFalse,
				Reason:  v1alpha1.ReasonErrored,
				Message: "set secret failed: secret key unexisting does not exist",
			}
			return checkCondition(ps.Status, expected)
		}
	}
	// if target Secret name is not specified it should use the ExternalSecret name.
	failNoSecretStore := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		tc.store = nil
		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			expected := v1alpha1.PushSecretStatusCondition{
				Type:    v1alpha1.PushSecretReady,
				Status:  v1.ConditionFalse,
				Reason:  v1alpha1.ReasonErrored,
				Message: "could not get SecretStore \"test-store\", secretstores.external-secrets.io \"test-store\" not found",
			}
			return checkCondition(ps.Status, expected)
		}
	}
	// if target Secret name is not specified it should use the ExternalSecret name.
	failNoClusterStore := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}
		tc.store = nil
		tc.pushsecret.Spec.SecretStoreRefs[0].Kind = "ClusterSecretStore"
		tc.pushsecret.Spec.SecretStoreRefs[0].Name = "unexisting"
		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			expected := v1alpha1.PushSecretStatusCondition{
				Type:    v1alpha1.PushSecretReady,
				Status:  v1.ConditionFalse,
				Reason:  v1alpha1.ReasonErrored,
				Message: "could not get ClusterSecretStore \"unexisting\", clustersecretstores.external-secrets.io \"unexisting\" not found",
			}
			return checkCondition(ps.Status, expected)
		}
	}
	// if target Secret name is not specified it should use the ExternalSecret name.
	setSecretFail := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return errors.New("boom")
		}
		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			expected := v1alpha1.PushSecretStatusCondition{
				Type:    v1alpha1.PushSecretReady,
				Status:  v1.ConditionFalse,
				Reason:  v1alpha1.ReasonErrored,
				Message: "set secret failed: could not write remote ref key to target secretstore test-store: boom",
			}
			return checkCondition(ps.Status, expected)
		}
	}
	// if target Secret name is not specified it should use the ExternalSecret name.
	newClientFail := func(tc *testCase) {
		fakeProvider.NewFn = func(context.Context, esv1.GenericStore, client.Client, string) (esv1.SecretsClient, error) {
			return nil, errors.New("boom")
		}
		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			expected := v1alpha1.PushSecretStatusCondition{
				Type:    v1alpha1.PushSecretReady,
				Status:  v1.ConditionFalse,
				Reason:  v1alpha1.ReasonErrored,
				Message: "set secret failed: could not get secrets client for store test-store: boom",
			}
			return checkCondition(ps.Status, expected)
		}
	}

	DescribeTable("When reconciling a PushSecret",
		func(tweaks ...testTweaks) {
			tc := makeDefaultTestcase()
			for _, tweak := range tweaks {
				tweak(tc)
			}
			ctx := context.Background()
			By("creating a secret store, secret and pushsecret")
			if tc.store != nil {
				Expect(k8sClient.Create(ctx, tc.store)).To(Succeed())
			}
			if tc.secret != nil {
				Expect(k8sClient.Create(ctx, tc.secret)).To(Succeed())
			}
			if tc.pushsecret != nil {
				Expect(k8sClient.Create(ctx, tc.pushsecret)).Should(Succeed())
			}
			time.Sleep(2 * time.Second) // prevents race conditions during tests causing failures
			psKey := types.NamespacedName{Name: PushSecretName, Namespace: PushSecretNamespace}
			createdPS := &v1alpha1.PushSecret{}
			By("checking the pushSecret condition")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, psKey, createdPS)
				if err != nil {
					return false
				}
				return tc.assert(createdPS, tc.secret)
			}, timeout, interval).Should(BeTrue())
			// this must be optional so we can test faulty es configuration
		},
		Entry("should sync", syncSuccessfully),
		Entry("should not update existing secret if UpdatePolicy=IfNotExists", updateIfNotExists),
		Entry("should only update parts of secret that don't already exist if UpdatePolicy=IfNotExists", updateIfNotExistsPartialSecrets),
		Entry("should update the PushSecret status correctly if UpdatePolicy=IfNotExists", updateIfNotExistsSyncStatus),
		Entry("should fail if secret existence cannot be verified if UpdatePolicy=IfNotExists", updateIfNotExistsSyncFailed),
		Entry("should sync with template", syncSuccessfullyWithTemplate),
		Entry("should sync with template reusing keys", syncSuccessfullyReusingKeys),
		Entry("should sync with conversion strategy", syncSuccessfullyWithConversionStrategy),
		Entry("should delete if DeletionPolicy=Delete", syncAndDeleteSuccessfully),
		Entry("should delete after DeletionPolicy changed from Delete to None", syncChangePolicyAndDeleteSuccessfully),
		Entry("should track deletion tasks if Delete fails", failDelete),
		Entry("should track deleted stores if Delete fails", failDeleteStore),
		Entry("should delete all secrets if SecretStore changes", deleteWholeStore),
		Entry("should sync to stores matching labels", syncMatchingLabels),
		Entry("should sync with ClusterStore", syncWithClusterStore),
		Entry("should sync with ClusterStore matching labels", syncWithClusterStoreMatchingLabels),
		Entry("should sync with Generator", syncWithGenerator),
		Entry("should fail if Secret is not created", failNoSecret),
		Entry("should fail if Secret Key does not exist", failNoSecretKey),
		Entry("should fail if SetSecret fails", setSecretFail),
		Entry("should fail if no valid SecretStore", failNoSecretStore),
		Entry("should fail if no valid ClusterSecretStore", failNoClusterStore),
		Entry("should fail if NewClient fails", newClientFail),
	)
})

var _ = Describe("PushSecret Controller Un/Managed Stores", func() {
	const (
		PushSecretName            = "test-ps"
		ManagedPushSecretStore1   = "test-managed-store-1"
		ManagedPushSecretStore2   = "test-managed-store-2"
		UnmanagedPushSecretStore1 = "test-unmanaged-store-1"
		UnmanagedPushSecretStore2 = "test-unmanaged-store-2"
		SecretName                = "test-secret"
	)

	var PushSecretNamespace string
	PushSecretStores := []string{ManagedPushSecretStore1, ManagedPushSecretStore2, UnmanagedPushSecretStore1, UnmanagedPushSecretStore2}

	// if we are in debug and need to increase the timeout for testing, we can do so by using an env var
	if customTimeout := os.Getenv("TEST_CUSTOM_TIMEOUT_SEC"); customTimeout != "" {
		if t, err := strconv.Atoi(customTimeout); err == nil {
			timeout = time.Second * time.Duration(t)
		}
	}

	BeforeEach(func() {
		var err error
		PushSecretNamespace, err = ctest.CreateNamespace("test-ns", k8sClient)
		Expect(err).ToNot(HaveOccurred())
		fakeProvider.Reset()
	})

	AfterEach(func() {
		k8sClient.Delete(context.Background(), &v1alpha1.PushSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      PushSecretName,
				Namespace: PushSecretNamespace,
			},
		})
		// give a time for reconciler to remove finalizers before removing SecretStores
		// TODO: Secret Stores should have finalizers bound to PushSecrets if DeletionPolicy == Delete
		time.Sleep(2 * time.Second)
		for _, psstore := range PushSecretStores {
			k8sClient.Delete(context.Background(), &esv1.SecretStore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      psstore,
					Namespace: PushSecretNamespace,
				},
			})
			k8sClient.Delete(context.Background(), &esv1.ClusterSecretStore{
				ObjectMeta: metav1.ObjectMeta{
					Name: psstore,
				},
			})
		}
		k8sClient.Delete(context.Background(), &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      SecretName,
				Namespace: PushSecretNamespace,
			},
		})
		Expect(k8sClient.Delete(context.Background(), &v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: PushSecretNamespace,
			},
		})).To(Succeed())
	})

	const (
		defaultKey  = "key"
		defaultVal  = "value"
		defaultPath = "path/to/key"
	)

	makeDefaultTestcase := func() *testCase {
		return &testCase{
			pushsecret: &v1alpha1.PushSecret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      PushSecretName,
					Namespace: PushSecretNamespace,
				},
				Spec: v1alpha1.PushSecretSpec{
					SecretStoreRefs: []v1alpha1.PushSecretStoreRef{
						{
							Name: ManagedPushSecretStore1,
							Kind: "SecretStore",
						},
					},
					Selector: v1alpha1.PushSecretSelector{
						Secret: &v1alpha1.PushSecretSecret{
							Name: SecretName,
						},
					},
					Data: []v1alpha1.PushSecretData{
						{
							Match: v1alpha1.PushSecretMatch{
								SecretKey: defaultKey,
								RemoteRef: v1alpha1.PushSecretRemoteRef{
									RemoteKey: defaultPath,
								},
							},
						},
					},
				},
			},
			secret: &v1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      SecretName,
					Namespace: PushSecretNamespace,
				},
				Data: map[string][]byte{
					defaultKey: []byte(defaultVal),
				},
			},
			managedStore1: &esv1.SecretStore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      ManagedPushSecretStore1,
					Namespace: PushSecretNamespace,
				},
				TypeMeta: metav1.TypeMeta{
					Kind: "SecretStore",
				},
				Spec: esv1.SecretStoreSpec{
					Provider: &esv1.SecretStoreProvider{
						Fake: &esv1.FakeProvider{
							Data: []esv1.FakeProviderData{},
						},
					},
				},
			},
			managedStore2: &esv1.SecretStore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      ManagedPushSecretStore2,
					Namespace: PushSecretNamespace,
				},
				TypeMeta: metav1.TypeMeta{
					Kind: "SecretStore",
				},
				Spec: esv1.SecretStoreSpec{
					Provider: &esv1.SecretStoreProvider{
						Fake: &esv1.FakeProvider{
							Data: []esv1.FakeProviderData{},
						},
					},
				},
			},
			unmanagedStore1: &esv1.SecretStore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      UnmanagedPushSecretStore1,
					Namespace: PushSecretNamespace,
				},
				TypeMeta: metav1.TypeMeta{
					Kind: "SecretStore",
				},
				Spec: esv1.SecretStoreSpec{
					Provider: &esv1.SecretStoreProvider{
						Fake: &esv1.FakeProvider{
							Data: []esv1.FakeProviderData{},
						},
					},
					Controller: "not-managed",
				},
			},
			unmanagedStore2: &esv1.SecretStore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      UnmanagedPushSecretStore2,
					Namespace: PushSecretNamespace,
				},
				TypeMeta: metav1.TypeMeta{
					Kind: "SecretStore",
				},
				Spec: esv1.SecretStoreSpec{
					Provider: &esv1.SecretStoreProvider{
						Fake: &esv1.FakeProvider{
							Data: []esv1.FakeProviderData{},
						},
					},
					Controller: "not-managed",
				},
			},
		}
	}

	multipleManagedStoresSyncsSuccessfully := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}

		tc.pushsecret.Spec.SecretStoreRefs = append(tc.pushsecret.Spec.SecretStoreRefs,
			v1alpha1.PushSecretStoreRef{
				Name: ManagedPushSecretStore2,
				Kind: "SecretStore",
			},
		)

		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			Eventually(func() bool {
				By("checking if Provider value got updated")
				secretValue := secret.Data[defaultKey]
				providerValue, ok := fakeProvider.SetSecretArgs[ps.Spec.Data[0].Match.RemoteRef.RemoteKey]
				if !ok {
					return false
				}
				got := providerValue.Value
				return bytes.Equal(got, secretValue)
			}, time.Second*10, time.Second).Should(BeTrue())
			return true
		}
	}

	skipUnmanagedStores := func(tc *testCase) {
		tc.pushsecret.Spec.SecretStoreRefs = []v1alpha1.PushSecretStoreRef{
			{
				Name: UnmanagedPushSecretStore1,
				Kind: "SecretStore",
			},
			{
				Name: UnmanagedPushSecretStore2,
				Kind: "SecretStore",
			},
		}

		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			return len(ps.Status.Conditions) == 0
		}
	}

	warnUnmanagedStoresAndSyncManagedStores := func(tc *testCase) {
		fakeProvider.SetSecretFn = func() error {
			return nil
		}

		tc.pushsecret.Spec.SecretStoreRefs = []v1alpha1.PushSecretStoreRef{
			{
				Name: ManagedPushSecretStore1,
				Kind: "SecretStore",
			},
			{
				Name: ManagedPushSecretStore2,
				Kind: "SecretStore",
			},
			{
				Name: UnmanagedPushSecretStore1,
				Kind: "SecretStore",
			},
			{
				Name: UnmanagedPushSecretStore2,
				Kind: "SecretStore",
			},
		}

		tc.assert = func(ps *v1alpha1.PushSecret, secret *v1.Secret) bool {
			Eventually(func() bool {
				By("checking if Provider value got updated")
				secretValue := secret.Data[defaultKey]
				providerValue, ok := fakeProvider.SetSecretArgs[ps.Spec.Data[0].Match.RemoteRef.RemoteKey]
				if !ok {
					return false
				}
				got := providerValue.Value
				return bytes.Equal(got, secretValue)
			}, time.Second*10, time.Second).Should(BeTrue())
			return true
		}
	}

	DescribeTable("When reconciling a PushSecret with multiple secret stores",
		func(tweaks ...testTweaks) {
			tc := makeDefaultTestcase()
			for _, tweak := range tweaks {
				tweak(tc)
			}
			ctx := context.Background()
			By("creating secret stores, a secret and a pushsecret")
			if tc.managedStore1 != nil {
				Expect(k8sClient.Create(ctx, tc.managedStore1)).To(Succeed())
			}
			if tc.managedStore2 != nil {
				Expect(k8sClient.Create(ctx, tc.managedStore2)).To(Succeed())
			}
			if tc.unmanagedStore1 != nil {
				Expect(k8sClient.Create(ctx, tc.unmanagedStore1)).To(Succeed())
			}
			if tc.unmanagedStore2 != nil {
				Expect(k8sClient.Create(ctx, tc.unmanagedStore2)).To(Succeed())
			}
			if tc.secret != nil {
				Expect(k8sClient.Create(ctx, tc.secret)).To(Succeed())
			}
			if tc.pushsecret != nil {
				Expect(k8sClient.Create(ctx, tc.pushsecret)).Should(Succeed())
			}
			time.Sleep(2 * time.Second) // prevents race conditions during tests causing failures
			psKey := types.NamespacedName{Name: PushSecretName, Namespace: PushSecretNamespace}
			createdPS := &v1alpha1.PushSecret{}
			By("checking the pushSecret condition")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, psKey, createdPS)
				if err != nil {
					return false
				}
				return tc.assert(createdPS, tc.secret)
			}, timeout, interval).Should(BeTrue())
			// this must be optional so we can test faulty es configuration
		},
		Entry("should sync successfully if there are multiple managed stores", multipleManagedStoresSyncsSuccessfully),
		Entry("should skip unmanaged stores", skipUnmanagedStores),
		Entry("should skip unmanaged stores and sync managed stores", warnUnmanagedStoresAndSyncManagedStores),
	)
})
