package test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var f *Framework

func TestFissionCLI(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Worker Fission Suite")
}

var _ = BeforeSuite(func() {
	f = NewFramework()
	err := f.Start(context.Background())
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	err := f.Stop()
	Expect(err).NotTo(HaveOccurred())
})
