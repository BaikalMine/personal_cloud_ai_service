package loratraining

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDecodeHTTPErrorPreservesMachineCode(t *testing.T) {
	response := &http.Response{
		StatusCode: http.StatusNotFound,
		Body:       io.NopCloser(strings.NewReader(`{"code":"job_not_found","message":"Задание не найдено."}`)),
	}
	err := decodeHTTPError(response)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("decodeHTTPError() = %T, want *HTTPError", err)
	}
	if httpErr.StatusCode != http.StatusNotFound || httpErr.Code != "job_not_found" {
		t.Fatalf("decoded HTTP error = %+v", httpErr)
	}
}
