package poolmgr

import (
	"testing"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/go-logr/logr"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestProvisioner_effectiveTarget(t *testing.T) {
	tests := []struct {
		name string // description of this test case
		// Named input parameters for receiver constructor.
		logger           logr.Logger
		gpm              *GenericPoolManager
		fissionClient    versioned.Interface
		kubernetesClient kubernetes.Interface
		crClient         client.Client
		config           ProvisionerConfig
		// Named input parameters for target function.
		fn   *fv1.Function
		want int
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewProvisioner(tt.logger, tt.gpm, tt.fissionClient, tt.kubernetesClient, tt.crClient, tt.config)
			got := p.effectiveTarget(tt.fn)
			// TODO: update the condition below to compare got with tt.want.
			if true {
				t.Errorf("effectiveTarget() = %v, want %v", got, tt.want)
			}
		})
	}
}
