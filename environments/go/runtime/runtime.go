package runtime

type (
  Context map[string]interface{}
)

func NewContext() Context {
  ctx := make(map[string]interface{})
  return ctx
}

func GetParams(ctx Context) map[string]string {
  return ctx["params"].(map[string]string)
}
