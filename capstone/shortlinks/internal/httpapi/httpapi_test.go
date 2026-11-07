package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"shortlinks/internal/links"
)

func TestWriteErrorMapping(t *testing.T) {
	cases := map[error]int{
		links.ErrInvalidURL:                400,
		links.ErrNotFound:                  404,
		context.DeadlineExceeded:           503,
		errors.New("pg: connection reset"): 500,
	}
	for err, want := range cases {
		rec := httptest.NewRecorder()
		writeError(rec, httptest.NewRequest(http.MethodGet, "/x", nil), err)
		if rec.Code != want {
			t.Errorf("%v: %d, ожидался %d", err, rec.Code, want)
		}
		if want == 500 && rec.Body.String() != "{\"error\":\"внутренняя ошибка\"}\n" {
			t.Errorf("подробности внутренней ошибки ушли клиенту: %s", rec.Body.String())
		}
	}
}
