package function

import "testing"

type pkgRequest struct {
	uid    string
	fnName string
}

func TestGeneratePackageName(t *testing.T) {
	for _, test := range []struct {
		name     string
		request  *pkgRequest
		expected int
	}{
		{
			name: "name of package should be less than or equal to 63 characters, if function name is less than or equal to 26 characters",
			request: &pkgRequest{
				uid:    "35764439-0676-48bf-bb61-c3cdcc82b801",
				fnName: "testfunctionwith26character",
			},
			expected: 63,
		},
		{
			name: "name of package should be less than or equal to 63 characters, if function name is more than 26 characters",
			request: &pkgRequest{
				uid:    "cea48e99-cd94-4ee0-bd9d-a249fa8d70a0",
				fnName: "testfunctionwithmorethan26character",
			},
			expected: 63,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pkgName := generatePackageName(test.request.fnName, test.request.uid)
			if len(pkgName) > test.expected {
				t.Errorf("expected len of package to be %v, got %v", test.expected, len(pkgName))
			}
		})
	}
}
