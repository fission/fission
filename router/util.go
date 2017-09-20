package router

import (
	"net/http"
	"k8s.io/client-go/1.5/pkg/api"
	"fmt"
	"k8s.io/client-go/1.5/pkg/types"
)

const (
	HEADERS_FISSION_FUNCTION_PREFIX = "Fission-Function"
)

func MetadataToHeaders(prefix string, meta *api.ObjectMeta, request *http.Request) error {
	request.Header.Add(fmt.Sprintf("X-%s-Uid", prefix), string(meta.UID))
	request.Header.Add(fmt.Sprintf("X-%s-Name", prefix), meta.Name)
	request.Header.Add(fmt.Sprintf("X-%s-Namespace", prefix), meta.Namespace)
	request.Header.Add(fmt.Sprintf("X-%s-ResourceVersion", prefix), meta.ResourceVersion)
	return nil
}

func HeadersToMetadata(prefix string, headers http.Header) (*api.ObjectMeta, error) {
	meta := &api.ObjectMeta{
		Name:            headers.Get(fmt.Sprintf("X-%s-Name", prefix)),
		UID:             types.UID(headers.Get(fmt.Sprintf("X-%s-Uid", prefix))),
		Namespace:       headers.Get(fmt.Sprintf("X-%s-Namespace", prefix)),
		ResourceVersion: headers.Get(fmt.Sprintf("X-%s-ResourceVersion", prefix)),
	}

	return meta, nil
}
