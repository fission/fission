package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"time"

	fissionv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/spf13/pflag"

	admv1beta1 "k8s.io/api/admission/v1beta1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/server/options"
	"k8s.io/component-base/cli/globalflag"
)

type Options struct {
	SecureServingOptions options.SecureServingOptions
}

func (o *Options) AddFlagSet(fs *pflag.FlagSet) {
	o.SecureServingOptions.AddFlags(fs)
}

type Config struct {
	SecureServingInfo *server.SecureServingInfo
}

func (o *Options) Config() *Config {
	if err := o.SecureServingOptions.MaybeDefaultWithSelfSignedCerts("0.0.0.0", nil, nil); err != nil {
		panic(err)
	}

	c := Config{}
	o.SecureServingOptions.ApplyTo(&c.SecureServingInfo)
	return &c
}

const (
	valKon = "val-kontroller"
)

func NewDefaultOptions() *Options {
	o := &Options{
		SecureServingOptions: *options.NewSecureServingOptions(),
	}
	o.SecureServingOptions.BindPort = 8443
	o.SecureServingOptions.ServerCert.PairName = valKon
	return o
}

func main() {
	options := NewDefaultOptions()

	fs := pflag.NewFlagSet(valKon, pflag.ExitOnError)
	globalflag.AddGlobalFlags(fs, valKon)

	options.AddFlagSet(fs)

	if err := fs.Parse(os.Args); err != nil {
		panic(err)
	}

	c := options.Config()

	mux := http.NewServeMux()
	mux.Handle("/", http.HandlerFunc(ServeKlusterValidation))

	stopCh := server.SetupSignalHandler()
	ch, _, err := c.SecureServingInfo.Serve(mux, 30*time.Second, stopCh)
	if err != nil {
		panic(err)
	} else {
		<-ch
	}
}

var (
	scheme = runtime.NewScheme()
	codecs = serializer.NewCodecFactory(scheme)
)

func ServeKlusterValidation(w http.ResponseWriter, r *http.Request) {
	fmt.Println("ServeKlusterValidation was called")

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		responsewriters.InternalError(w, r, err)
		fmt.Printf("Error %s, reading the body", err.Error())
	}

	// read body to get an instance of admissionreivew object
	// get the gvk for admission review
	gvk := admv1beta1.SchemeGroupVersion.WithKind("AdmissionReview")
	// var of type admission reiveew
	var admissionReview admv1beta1.AdmissionReview
	_, _, err = codecs.UniversalDeserializer().Decode(body, &gvk, &admissionReview)
	if err != nil {
		fmt.Printf("Error %s, converting req body to admission review type", err.Error())

	}

	response := admv1beta1.AdmissionResponse{}
	allow := validateKluster(admissionReview.Request.Object.Raw)
	// if allow is false or err is not nil
	if !allow {
		response = admv1beta1.AdmissionResponse{
			UID:     admissionReview.Request.UID,
			Allowed: allow,
			Result: &v1.Status{
				Message: fmt.Sprintf("This is not valid env"),
			},
		}
	} else {
		response = admv1beta1.AdmissionResponse{
			UID:     admissionReview.Request.UID,
			Allowed: allow,
		}
	}

	admissionReview.Response = &response
	// write the response to response writer
	fmt.Printf("respoknse that we are trying to return is %+v\n", response)
	res, err := json.Marshal(admissionReview)
	if err != nil {
		fmt.Printf("error %s, while converting response to byte slice", err.Error())
	}

	_, err = w.Write(res)
	if err != nil {
		fmt.Printf("error %s, writing respnse to responsewriter", err.Error())
	}
}

func validateKluster(spek []byte) bool {

	// get kluster spec from admission review object
	gvkKluster := fissionv1.SchemeGroupVersion.WithKind("Environment")
	var k fissionv1.Environment
	// var gvr = GroupVersionKind{}
	_, _, err := codecs.UniversalDeserializer().Decode(spek, &gvkKluster, &k)
	if err != nil {
		fmt.Printf("Error %s, while getting kluster type from admission review", err.Error())
	}

	fmt.Println("Hey we are here")
	log.Println("Hey we are here")

	err = k.Spec.Validate()
	if err != nil {
		return false
	}

	// log.Println("env %s is valid", env.Name)
	return true
}
