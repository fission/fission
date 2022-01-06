package util

import (
	"strings"
)

func IsNotFound(err error) bool {
	var result bool = strings.HasSuffix(strings.TrimSpace(err.Error()), "not found")
	return result
}
