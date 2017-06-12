package poolmgr

import (
	"k8s.io/client-go/1.5/pkg/api/resource"

	"github.com/fission/fission"
)

type EnvResources struct {
	cpuLimit   resource.Quantity
	memLimit   resource.Quantity
	cpuRequest resource.Quantity
	memRequest resource.Quantity
}

var (
	FETCHER_MEM_LIMIT, _   = resource.ParseQuantity("64Mi")
	FETCHER_CPU_LIMIT, _   = resource.ParseQuantity("50m")
	FETCHER_MEM_REQUEST, _ = resource.ParseQuantity("64Mi")
	FETCHER_CPU_REQUEST, _ = resource.ParseQuantity("10m")
	DEFAULT_MEM_LIMIT, _   = resource.ParseQuantity("256Mi")
	DEFAULT_CPU_LIMIT, _   = resource.ParseQuantity("100m")
	DEFAULT_MEM_REQUEST, _ = resource.ParseQuantity("128Mi")
	DEFAULT_CPU_REQUEST, _ = resource.ParseQuantity("50m")
	DEFAULT_MEM_UPPER, _   = resource.ParseQuantity("1024Mi")
	DEFAULT_CPU_UPPER, _   = resource.ParseQuantity("1000m")
	DEFAULT_MEM_LOWER, _   = resource.ParseQuantity("8Mi")
	DEFAULT_CPU_LOWER, _   = resource.ParseQuantity("1m")
)

func EnsureCpuQuantityInRange(quantity resource.Quantity) resource.Quantity {
	if quantity.Cmp(DEFAULT_CPU_LOWER) == -1 {
		return DEFAULT_CPU_LOWER
	}
	if quantity.Cmp(DEFAULT_CPU_UPPER) == 1 {
		return DEFAULT_CPU_UPPER
	}
	return quantity
}

func EnsureMemQuantityInRange(quantity resource.Quantity) resource.Quantity {
	if quantity.Cmp(DEFAULT_MEM_LOWER) == -1 {
		return DEFAULT_MEM_LOWER
	}
	if quantity.Cmp(DEFAULT_MEM_UPPER) == 1 {
		return DEFAULT_MEM_UPPER
	}
	return quantity
}

func GetResourceQuantity(env *fission.Environment) *EnvResources {
	res := EnvResources{}
	var err error
	res.cpuLimit, err = resource.ParseQuantity(env.CpuLimit)
	if err != nil {
		res.cpuLimit = DEFAULT_CPU_LIMIT
	} else {
		res.cpuLimit = EnsureCpuQuantityInRange(res.cpuLimit)
	}
	res.memLimit, err = resource.ParseQuantity(env.MemLimit)
	if err != nil {
		res.memLimit = DEFAULT_MEM_LIMIT
	} else {
		res.memLimit = EnsureMemQuantityInRange(res.memLimit)
	}
	res.cpuRequest, err = resource.ParseQuantity(env.CpuRequest)
	if err != nil {
		res.cpuRequest = DEFAULT_CPU_REQUEST
	} else {
		res.cpuRequest = EnsureCpuQuantityInRange(res.cpuRequest)
	}
	res.memRequest, err = resource.ParseQuantity(env.MemRequest)
	if err != nil {
		res.memRequest = DEFAULT_MEM_REQUEST
	} else {
		res.memRequest = EnsureMemQuantityInRange(res.memRequest)
	}

	if res.cpuLimit.Cmp(res.cpuRequest) == -1 {
		res.cpuLimit = res.cpuRequest
	}
	if res.memLimit.Cmp(res.memRequest) == -1 {
		res.memLimit = res.memRequest
	}

	return &res
}
