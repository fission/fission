package runtime

const (
  PARAMS = "gorilla/mux/params"
)

type (
  Context map[string]interface{}
)

func NewContext() Context {
  ctx := make(Context)
  return ctx
}
