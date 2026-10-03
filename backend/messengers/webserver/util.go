package webserver

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/juju/errors"

	"github.com/dimonomid/montray/interror"
	"github.com/dimonomid/montray/logs"
)

var (
	internalServerError = errors.New("internal server error")
)

const (
	desiredContentTypeKey = "desiredContentType"
	headerNamePrettyJSON  = "X-Pretty-JSON"
)

type errorResponse struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
}

func makeAPIHandlerWWriter(
	logger *logs.Logger,
	f func(w http.ResponseWriter, r *http.Request) (resp interface{}, err error),
) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		resp, err := f(w, r)

		// If both resp and err are nil, it means the connection was "hijacked" and
		// we shouldn't send anything more there.
		if resp == nil && err == nil {
			return
		}

		handleAPIRespErr(logger, w, r, resp, err)
	}
}

func handleAPIRespErr(logger *logs.Logger, w http.ResponseWriter, r *http.Request, resp interface{}, err error) {
	if err != nil {
		respondWithError(logger, w, r, errors.Trace(err))
		return
	}

	var d []byte
	if r.Header.Get(headerNamePrettyJSON) == "1" {
		d, err = json.MarshalIndent(resp, "", "  ")
	} else {
		d, err = json.Marshal(resp)
	}
	if err != nil {
		respondWithError(logger, w, r, makeInternalServerError(
			errors.Annotatef(err, "marshalling resp"),
		))
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(d)
	if err != nil {
		panic(err)
	}
}

func getErrorStruct(errResp error) *errorResponse {
	httpErrorCode := getHTTPErrorCode(errResp)
	return &errorResponse{
		Status:  httpErrorCode,
		Message: errResp.Error(),
	}
}

func getHTTPErrorCode(err error) int {
	status := http.StatusBadRequest

	switch errors.Cause(err) {
	case internalServerError:
		status = http.StatusInternalServerError
	}

	return status
}

func respondWithError(logger *logs.Logger, w http.ResponseWriter, r *http.Request, errResp error) {
	errStruct := getErrorStruct(errResp)

	desiredContentType := "text/html"

	if errors.Cause(errResp) == internalServerError {
		logger.Log(logs.Error, "Internal HTTP server error: %s", interror.ErrorStack(errResp))
	}

	v := r.Context().Value(desiredContentTypeKey)
	if v != nil {
		var ok bool
		desiredContentType, ok = v.(string)
		if !ok {
			//glog.Errorf("wrong type of desiredContentType: %T (%v)",
			//desiredContentType, desiredContentType)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}

	switch desiredContentType {
	case "application/json":
		d, err := json.MarshalIndent(errStruct, "", "  ")
		if err != nil {
			panic(err)
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(errStruct.Status)
		_, err = w.Write(d)
		if err != nil {
			panic(err)
		}
	case "text/html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(errStruct.Status)
		_, err := w.Write([]byte("Error: " + errResp.Error()))
		if err != nil {
			panic(err)
		}
	default:
		//glog.Errorf("wrong desiredContentType: %q", desiredContentType)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

// makeInternalServerError logs the given error and returns internalServerError
// annotated with the message, which does NOT wrap the original error, since we
// don't want internal server error details to percolate to clients.
func makeInternalServerError(intError error) error {
	if errors.Cause(intError) != internalServerError {
		return interror.WrapInternalError(intError, internalServerError)
	}
	return errors.Trace(intError)
}

type genericMiddleware struct {
	f func(w http.ResponseWriter, r *http.Request)
}

func (h *genericMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.f(w, r)
}

func mkMiddleware(f func(w http.ResponseWriter, r *http.Request)) http.Handler {
	return &genericMiddleware{
		f: f,
	}
}

func makeDesiredContentTypeMiddleware(
	contentType string,
) func(inner http.Handler) http.Handler {
	return func(inner http.Handler) http.Handler {
		mw := func(w http.ResponseWriter, r *http.Request) {
			// Process request
			inner.ServeHTTP(w, r.WithContext(context.WithValue(
				r.Context(), desiredContentTypeKey, contentType,
			)))
		}
		return mkMiddleware(mw)
	}
}
