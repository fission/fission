package router

import (
	"go.uber.org/zap"
	"k8s.io/client-go/rest"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
)

type RecorderSet struct {
	logger *zap.Logger

	httpTriggerSet *HTTPTriggerSet

	crdClient *rest.RESTClient

	recStore      k8sCache.Store
	recController k8sCache.Controller

	functionRecorderMap *functionRecorderMap
	triggerRecorderMap  *triggerRecorderMap
}

func MakeRecorderSet(logger *zap.Logger, httpTriggerSet *HTTPTriggerSet, crdClient *rest.RESTClient, rStore k8sCache.Store, frmap *functionRecorderMap, trmap *triggerRecorderMap) *RecorderSet {
	recorderSet := &RecorderSet{
		logger:              logger.Named("recorder_set"),
		httpTriggerSet:      httpTriggerSet,
		crdClient:           crdClient,
		recStore:            rStore,
		functionRecorderMap: frmap,
		triggerRecorderMap:  trmap,
	}
	recorderSet.recStore, recorderSet.recController = httpTriggerSet.initRecorderController()
	return recorderSet
}

// All new recorders are by default enabled
func (rs *RecorderSet) newRecorder(r *fv1.Recorder) {
	function := r.Spec.Function
	triggers := r.Spec.Triggers

	// If triggers are not explicitly specified during the creation of this recorder,
	// keep track of those associated with the function specified [implicitly added triggers]
	needTrackByFunction := len(triggers) == 0

	rs.functionRecorderMap.assign(function, r)

	if needTrackByFunction {
		for _, t := range rs.httpTriggerSet.triggerStore.List() {
			trigger := *t.(*fv1.HTTPTrigger)
			if trigger.Spec.FunctionReference.Name == function {
				rs.triggerRecorderMap.assign(trigger.Metadata.Name, r)
			}
		}
	} else {
		for _, trigger := range triggers {
			rs.triggerRecorderMap.assign(trigger, r)
		}
	}

	rs.httpTriggerSet.syncTriggers()
}

// TODO: Delete or disable?
func (rs *RecorderSet) disableRecorder(r *fv1.Recorder) {
	function := r.Spec.Function
	triggers := r.Spec.Triggers

	rs.logger.Info("disabling recorder",
		zap.String("recorder", r.Metadata.Name),
		zap.String("function", function))

	// Account for function
	err := rs.functionRecorderMap.remove(function)
	if err != nil {
		rs.logger.Error("error disabling recorder (failed to remove function from functionRecorderMap)",
			zap.Error(err),
			zap.String("recorder", r.Metadata.Name),
			zap.String("function", function))
	}

	// Account for explicitly added triggers
	if len(triggers) != 0 {
		for _, trigger := range triggers {
			err := rs.triggerRecorderMap.remove(trigger)
			if err != nil {
				rs.logger.Error("error disabling recorder (failed to remove triggers from triggerRecorderMap)",
					zap.Error(err),
					zap.String("recorder", r.Metadata.Name),
					zap.String("function", function),
					zap.String("trigger", trigger))
			}
		}
	} else {
		// Account for implicitly added triggers
		for _, t := range rs.httpTriggerSet.triggerStore.List() {
			trigger := *t.(*fv1.HTTPTrigger)
			if trigger.Spec.FunctionReference.Name == function {
				err := rs.triggerRecorderMap.remove(trigger.Metadata.Name)
				if err != nil {
					rs.logger.Error("failed to remove trigger from triggerRecorderMap",
						zap.Error(err),
						zap.String("recorder", r.Metadata.Name),
						zap.String("function", function),
						zap.String("trigger", trigger.Metadata.Name))
				}
			}
		}
	}

	rs.httpTriggerSet.syncTriggers()
}

func (rs *RecorderSet) updateRecorder(old *fv1.Recorder, newer *fv1.Recorder) {
	if newer.Spec.Enabled == true {
		rs.newRecorder(newer) // TODO: Test this
	} else {
		rs.disableRecorder(old)
	}
}

func (rs *RecorderSet) DeleteTriggerFromRecorderMap(trigger *fv1.HTTPTrigger) {
	err := rs.triggerRecorderMap.remove(trigger.Metadata.Name)
	if err != nil {
		rs.logger.Error("failed to remove trigger from triggerRecorderMap", zap.Error(err))
	}
}

func (rs *RecorderSet) DeleteFunctionFromRecorderMap(function *fv1.Function) {
	err := rs.functionRecorderMap.remove(function.Metadata.Name)
	if err != nil {
		rs.logger.Error("failed to remove function from functionRecorderMap", zap.Error(err))
	}
}
