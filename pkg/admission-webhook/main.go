/*
Copyright 2022.

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

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.

	corev1 "github.com/fission/fission/pkg/apis/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	//+kubebuilder:scaffold:scheme
}

// EnvValidator annotates environment
type EnvValidator struct {
	Client  client.Client
	decoder *admission.Decoder
}

type HookParamters struct {
	certDir string
	port    int
}

func main() {

	var params HookParamters

	flag.IntVar(&params.port, "port", 8443, "Wehbook port")
	flag.StringVar(&params.certDir, "certDir", "/certs/", "Wehbook certificate folder")
	flag.Parse()

	// Setup a Manager
	log.Println("setting up manager")
	mgr, err := manager.New(config.GetConfigOrDie(), manager.Options{})
	if err != nil {
		log.Println(err, "unable to set up overall controller manager")
		os.Exit(1)
	}

	// Setup webhooks
	setupLog.Info("setting up webhook server")
	hookServer := mgr.GetWebhookServer()
	// hookServer.Start()

	hookServer.Port = params.port
	hookServer.CertDir = params.certDir

	setupLog.Info("registering webhooks to the webhook server")

	// hookServer.Register("/mutate-v1-pod", &webhook.Admission{Handler: &podAnnotator{Client: mgr.GetClient()}})
	hookServer.Register("environments/v1/validate", &webhook.Admission{Handler: &EnvValidator{Client: mgr.GetClient()}})

	setupLog.Info("starting manager")
	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "unable to run manager")
		os.Exit(1)
	}
}

// EnvValidator adds an annotation to every incoming pods.
func (v *EnvValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	env := &corev1.Environment{}

	err := v.decoder.Decode(req, env)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	err = env.Validate()
	if err != nil {
		return admission.Denied(fmt.Sprintf("could not validate due to  %s", err))
	}

	return admission.Denied(fmt.Sprintln("Fixed denial"))

	return admission.Allowed("")
}

// envAnnotator implements inject.Client.
// A client will be automatically injected.

// InjectClient injects the client.
func (a *EnvValidator) InjectClient(c client.Client) error {
	a.Client = c
	return nil
}

// podAnnotator implements admission.DecoderInjector.
// A decoder will be automatically injected.

// InjectDecoder injects the decoder.
func (a *EnvValidator) InjectDecoder(d *admission.Decoder) error {
	a.decoder = d
	return nil
}
