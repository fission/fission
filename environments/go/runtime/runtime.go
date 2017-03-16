package runtime

type (
  Context map[string]interface{}
)

func NewContext() Context {
  ctx := make(map[string]interface{})
  return ctx
}
