package util

import (
	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"time"
)

// UpdateFunctionStatus ...
func UpdateFunctionStatus(fn *fv1.Function, c fv1.CRCondition,
	fissionClient *crd.FissionClient, logger *zap.Logger) {
	foundAt := -1
	for i, con := range fn.Status.Conditions {
		if c.CRName == con.CRName {
			foundAt = i
			break
		}
	}
	if foundAt >= 0 {
		fn.Status.Conditions = append(fn.Status.Conditions[:foundAt], fn.Status.Conditions[foundAt+1:]...)
	}
	fn.Status.Conditions = append(fn.Status.Conditions, c)

	// Update function status
	_, err := fissionClient.CoreV1().Functions(fn.ObjectMeta.Namespace).Update(fn)
	if err != nil {
		logger.Error("error updating function status", zap.String("function_name", fn.ObjectMeta.Name), zap.Any("Error", err.Error()))
	}
	logger.Debug("Updated status of function:", zap.Any("function", fn.ObjectMeta.Name), zap.Any("updated_condition", c))
}

//GetPkgCondition ...
func GetPkgCondition(pkg *fv1.Package) fv1.CRCondition {
	pkgCondition := fv1.CRCondition{
		CRName:         "package",
		Status:         "false",
		LastUpdateTime: metav1.Time{Time: time.Now().UTC()},
	}
	switch pkg.Status.BuildStatus {
	case fv1.BuildStatusSucceeded:
		pkgCondition.Status = "true"
		pkgCondition.Type = fv1.CRReady
		pkgCondition.Message = pkg.Status.BuildLog

	case fv1.BuildStatusRunning:
		pkgCondition.Type = fv1.CRProgressing
		pkgCondition.Message = pkg.Status.BuildLog

	case fv1.BuildStatusPending:
		pkgCondition.Type = fv1.CRPending
		pkgCondition.Reason = pkg.Status.BuildLog

	case fv1.BuildStatusFailed:
		pkgCondition.Type = fv1.CRFailure
		pkgCondition.Reason = pkg.Status.BuildLog
	}

	return pkgCondition
}
