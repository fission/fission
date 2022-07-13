package function

import (
	"testing"

	uuid "github.com/satori/go.uuid"
)

func TestGeneratePackageName(t *testing.T) {
	for _, test := range []struct {
		name     string
		fnName   string
		expected int
	}{
		{
			name:     "name of package should be less than or equal to 63 characters, if function name is equal to 26 characters",
			fnName:   "test-function-with-26-char",
			expected: 63,
		},
		{
			name:     "name of package should be less than or equal to 63 characters, if function name is more than 26 characters",
			fnName:   "testfunctionwithmorethan26character",
			expected: 63,
		},
		{
			name:     "name of package should be less than or equal to 63 characters, if function name is less than 26 characters",
			fnName:   "fission-function",
			expected: 63,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			id, err := uuid.NewV4()
			if err != nil {
				t.Fatal(err)
			}
			pkgName := generatePackageName(test.fnName, id.String())
			if len(pkgName) > test.expected {
				t.Errorf("expected len of package to be %v, got %v", test.expected, len(pkgName))
			}
		})
	}
}
