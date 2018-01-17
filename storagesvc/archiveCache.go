package storagesvc

import (
	"github.com/fission/fission/cache"
)

type (
	ArchiveCache struct {
		cache *cache.Cache
	}
)

func MakeArchiveCache() *ArchiveCache {
	return &ArchiveCache{
		cache: cache.MakeCache(0, 0),
	}
}

func (archiveCache *ArchiveCache) Set() {

}

func (archiveCache *ArchiveCache) Get() {

}