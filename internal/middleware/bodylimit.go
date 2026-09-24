package middleware

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/prajwalmahajan101/busyapi/internal/errs"
	"github.com/prajwalmahajan101/busyapi/internal/response"
)

// BodyLimit rejects request bodies larger than maxBytes. A present, oversized
// Content-Length is rejected up front; the body is also wrapped in
// http.MaxBytesReader so the cap holds when Content-Length is absent, chunked,
// or understated. A read past the cap surfaces as *http.MaxBytesError, which is
// translated to a 413 envelope after the handler returns (handlers must push
// read/bind errors via c.Error).
func BodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > maxBytes {
			abortTooLarge(c)
			return
		}

		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()

		if c.Writer.Written() {
			return
		}

		for _, e := range c.Errors {
			var mbe *http.MaxBytesError
			if errors.As(e.Err, &mbe) {
				abortTooLarge(c)
				return
			}
		}
	}
}

func abortTooLarge(c *gin.Context) {
	response.Error(c, &errs.AppError{
		Code:       "payload_too_large",
		Message:    "request body too large",
		HTTPStatus: http.StatusRequestEntityTooLarge,
	})
	c.Abort()
}
