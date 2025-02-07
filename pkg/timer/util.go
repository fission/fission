package timer

import (
	"github.com/fission/fission/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/cache"
)

func getCacheOptions() cache.Options {
	fissionResourceNS := utils.GetNamespaces()

	cacheConfig := make(map[string]cache.Config)
	for _, namespace := range fissionResourceNS {
		cacheConfig[namespace] = cache.Config{}
	}

	return cache.Options{
		DefaultNamespaces: cacheConfig,
	}
}
