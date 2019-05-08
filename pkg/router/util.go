package router

import (
	"fmt"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	HEADERS_FISSION_FUNCTION_PREFIX = "Fission-Function"
)

func MetadataToHeaders(prefix string, meta *metav1.ObjectMeta, request *http.Request) {
	request.Header.Set(fmt.Sprintf("X-%s-Uid", prefix), string(meta.UID))
	request.Header.Set(fmt.Sprintf("X-%s-Name", prefix), meta.Name)
	request.Header.Set(fmt.Sprintf("X-%s-Namespace", prefix), meta.Namespace)
	request.Header.Set(fmt.Sprintf("X-%s-ResourceVersion", prefix), meta.ResourceVersion)
}

func HeadersToMetadata(prefix string, headers http.Header) *metav1.ObjectMeta {
	return &metav1.ObjectMeta{
		Name:            headers.Get(fmt.Sprintf("X-%s-Name", prefix)),
		UID:             types.UID(headers.Get(fmt.Sprintf("X-%s-Uid", prefix))),
		Namespace:       headers.Get(fmt.Sprintf("X-%s-Namespace", prefix)),
		ResourceVersion: headers.Get(fmt.Sprintf("X-%s-ResourceVersion", prefix)),
	}
}
