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

package aws

import (

	// nolint
	. "github.com/onsi/ginkgo/v2"

	"github.com/external-secrets/external-secrets-e2e/framework"
	awscommon "github.com/external-secrets/external-secrets-e2e/suites/provider/cases/aws"
	"github.com/external-secrets/external-secrets-e2e/suites/provider/cases/common"
	esapi "github.com/external-secrets/external-secrets/apis/externalsecrets/v1"
)

const (
	withStaticAuth         = "with static auth"
	withReferentStaticAuth = "with static referent auth"
)

var _ = Describe("[aws] ", Label("aws", "parameterstore"), func() {
	f := framework.New("eso-aws-ps")
	prov := NewFromEnv(f)

	DescribeTable("sync secrets",
		framework.TableFuncWithExternalSecret(f,
			prov),
		framework.Compose(withStaticAuth, f, common.SimpleDataSync, useStaticAuth),
		framework.Compose(withStaticAuth, f, common.NestedJSONWithGJSON, useStaticAuth),
		framework.Compose(withStaticAuth, f, common.JSONDataFromSync, useStaticAuth),
		framework.Compose(withStaticAuth, f, common.JSONDataFromRewrite, useStaticAuth),
		framework.Compose(withStaticAuth, f, common.JSONDataWithProperty, useStaticAuth),
		framework.Compose(withStaticAuth, f, common.JSONDataWithTemplate, useStaticAuth),
		framework.Compose(withStaticAuth, f, common.DockerJSONConfig, useStaticAuth),
		framework.Compose(withStaticAuth, f, common.DataPropertyDockerconfigJSON, useStaticAuth),
		framework.Compose(withStaticAuth, f, common.SSHKeySync, useStaticAuth),
		framework.Compose(withStaticAuth, f, common.SSHKeySyncDataProperty, useStaticAuth),
		framework.Compose(withStaticAuth, f, common.SyncWithoutTargetName, useStaticAuth),
		framework.Compose(withStaticAuth, f, common.JSONDataWithoutTargetName, useStaticAuth),

		framework.Compose(withStaticAuth, f, common.DeletionPolicyDelete, useStaticAuth),

		// referent auth
		framework.Compose(withReferentStaticAuth, f, common.SimpleDataSync, useReferentStaticAuth),

		// These are specific to parameterstore
		framework.Compose(withStaticAuth, f, FindByName, useStaticAuth),
		framework.Compose(withStaticAuth, f, FindByNameWithPath, useStaticAuth),
		framework.Compose(withStaticAuth, f, FindByTag, useStaticAuth),
		framework.Compose(withStaticAuth, f, FindByTagWithPath, useStaticAuth),
		framework.Compose(withStaticAuth, f, VersionedParameter(prov), useStaticAuth),
	)
})

func useStaticAuth(tc *framework.TestCase) {
	tc.ExternalSecret.Spec.SecretStoreRef.Name = awscommon.StaticStoreName
}

func useReferentStaticAuth(tc *framework.TestCase) {
	tc.ExternalSecret.Spec.SecretStoreRef.Name = awscommon.ReferentSecretStoreName(tc.Framework)
	tc.ExternalSecret.Spec.SecretStoreRef.Kind = esapi.ClusterSecretStoreKind
}
