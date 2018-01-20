package router

import "net/http"

type (
	// metav1.ObjectMeta is not hashable, so we make a hashable copy
	// of the subset of its fields that are identifiable.
	metadataKey struct {
		Name            string
		Namespace       string
		ResourceVersion string
	}

	LoggedResponse struct {
		http.ResponseWriter
		status int
	}
)
