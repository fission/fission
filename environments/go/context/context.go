package context

type (
	Context map[string]interface{}
)

func New() Context {
	ctx := make(map[string]interface{})
	return ctx
}
