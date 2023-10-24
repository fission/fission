package test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Fission environment tests", func() {
	ctx := context.Background()

	It("Should create a new environment", func() {
		_, err := f.ExecCommand(ctx, "env", "create", "--name", "test-env", "--image", "fission/python-env")
		Expect(err).NotTo(HaveOccurred())

		fissionClient, err := f.ClientGen().GetFissionClient()
		Expect(err).NotTo(HaveOccurred())
		// Verify that the environment was created successfully
		createdEnv, err := fissionClient.CoreV1().Environments(metav1.NamespaceDefault).Get(ctx, "test-env", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(createdEnv).NotTo(BeNil())
		Expect(createdEnv.Name).To(Equal("test-env"))
		Expect(createdEnv.Spec.Runtime.Image).To(Equal("fission/python-env"))
	})

	It("Should update an existing environment", func() {
		_, err := f.ExecCommand(ctx, "env", "update", "--name", "test-env", "--image", "fission/python-env:v2")
		Expect(err).NotTo(HaveOccurred())

		fissionClient, err := f.ClientGen().GetFissionClient()
		Expect(err).NotTo(HaveOccurred())
		// Verify that the environment was updated successfully
		updatedEnv, err := fissionClient.CoreV1().Environments(metav1.NamespaceDefault).Get(ctx, "test-env", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedEnv).NotTo(BeNil())
		Expect(updatedEnv.Name).To(Equal("test-env"))
		Expect(updatedEnv.Spec.Runtime.Image).To(Equal("fission/python-env:v2"))
	})

	It("Should delete an existing environment", func() {
		_, err := f.ExecCommand(ctx, "env", "delete", "--name", "test-env")
		Expect(err).NotTo(HaveOccurred())

		fissionClient, err := f.ClientGen().GetFissionClient()
		Expect(err).NotTo(HaveOccurred())
		// Verify that the environment was deleted successfully
		_, err = fissionClient.CoreV1().Environments(metav1.NamespaceDefault).Get(ctx, "test-env", metav1.GetOptions{})
		Expect(err).To(HaveOccurred())
	})

})
