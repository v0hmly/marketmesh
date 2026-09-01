package httpserver

import (
	"bufio"
	"io"
	"net"
	"net/http"
)

type observedResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func newObservedResponseWriter(response http.ResponseWriter) *observedResponseWriter {
	return &observedResponseWriter{
		ResponseWriter: response,
		status:         http.StatusOK,
	}
}

func (writer *observedResponseWriter) Status() int {
	return writer.status
}

func (writer *observedResponseWriter) WriteHeader(status int) {
	isInformational := status >= 100 && status <= 199 && status != http.StatusSwitchingProtocols
	if isInformational {
		writer.ResponseWriter.WriteHeader(status)
		return
	}
	if writer.wroteHeader {
		return
	}

	writer.status = status
	writer.wroteHeader = true
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *observedResponseWriter) Write(body []byte) (int, error) {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}

	return writer.ResponseWriter.Write(body)
}

func (writer *observedResponseWriter) ReadFrom(reader io.Reader) (int64, error) {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	if readerFrom, ok := writer.ResponseWriter.(io.ReaderFrom); ok {
		return readerFrom.ReadFrom(reader)
	}

	return io.Copy(writerOnly{Writer: writer}, reader)
}

func (writer *observedResponseWriter) Flush() {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}

	_ = http.NewResponseController(writer.ResponseWriter).Flush()
}

func (writer *observedResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(writer.ResponseWriter).Hijack()
}

func (writer *observedResponseWriter) Push(target string, options *http.PushOptions) error {
	pusher, ok := writer.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}

	return pusher.Push(target, options)
}

func (writer *observedResponseWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

type writerOnly struct {
	io.Writer
}
