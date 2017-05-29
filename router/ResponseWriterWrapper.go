package router

import "net/http"

// Create our own ResponseWriterWrapper to wrap a standard http.ResponseWriter
// so we can store the status code.
type ResponseWriterWrapper struct {
	status int
	size   int
	http.ResponseWriter
}

func NewResponseWriterWrapper(res http.ResponseWriter) *ResponseWriterWrapper {
	// Default the status code to 200
	return &ResponseWriterWrapper{200, 0, res}
}

// Give a way to get the status
func (w *ResponseWriterWrapper) Status() int {
	return w.status
}

func (w *ResponseWriterWrapper) ResponseSize() int {
	return w.size
}

// Satisfy the http.ResponseWriter interface
func (w *ResponseWriterWrapper) Header() http.Header {
	return w.ResponseWriter.Header()
}

func (w *ResponseWriterWrapper) Write(data []byte) (int, error) {
	size, err := w.ResponseWriter.Write(data)
	w.size = size
	return size, err
}

func (w *ResponseWriterWrapper) WriteHeader(statusCode int) {
	// Store the status code
	w.status = statusCode

	// Write the status code onward.
	w.ResponseWriter.WriteHeader(statusCode)
}
