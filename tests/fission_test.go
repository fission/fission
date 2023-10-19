package test

import (
	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Fission environment tests", func() {
	It("Should create a new environment", func() {
		// Create an environment
		env := &fv1.Environment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-env",
				Namespace: metav1.NamespaceDefault,
			},
			Spec: fv1.EnvironmentSpec{
				Runtime: fv1.Runtime{
					Image: "fission/python-env",
				},
				Version: 2,
			},
		}
		_, err := fissionClient.CoreV1().Environments(metav1.NamespaceDefault).Create(ctx, env, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())

		// Verify that the environment was created successfully
		createdEnv, err := fissionClient.CoreV1().Environments(metav1.NamespaceDefault).Get(ctx, "test-env", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(createdEnv).NotTo(BeNil())
		Expect(createdEnv.Name).To(Equal("test-env"))
		Expect(createdEnv.Spec.Runtime.Image).To(Equal("fission/python-env"))
		Expect(createdEnv.Spec.Version).To(Equal(2))
	})
})
