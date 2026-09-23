// Package response defines the single JSON envelope used by every endpoint and
// the helpers that write it. Error() resolves *errs.AppError to a status + body;
// unknown errors become a generic 500 with no internal detail leaked.
package response

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/prajwalmahajan101/busyapi/internal/errs"
	"github.com/prajwalmahajan101/busyapi/internal/reqcontext"
)

// ErrDetail is one error entry in the envelope's Errors list.
type ErrDetail struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"detail,omitempty"`
}

// Envelope is the uniform shape for every response.
type Envelope struct {
	Success   bool        `json:"success"`
	Message   string      `json:"message"`
	Data      any         `json:"data,omitempty"`
	Errors    []ErrDetail `json:"errors,omitempty"`
	RequestID string      `json:"request_id,omitempty"`
}

func requestID(c *gin.Context) string {
	return reqcontext.RequestIDFromContext(c.Request.Context())
}

// Success writes a 2xx envelope carrying data.
func Success(c *gin.Context, status int, msg string, data any) {
	c.JSON(status, Envelope{
		Success:   true,
		Message:   msg,
		Data:      data,
		RequestID: requestID(c),
	})
}

// Error resolves err to a status + envelope. *errs.AppError uses its own
// HTTPStatus/Code/Message/Details; any other error becomes a generic 500.
func Error(c *gin.Context, err error) {
	rid := requestID(c)

	var ae *errs.AppError
	if errors.As(err, &ae) {
		c.JSON(ae.HTTPStatus, Envelope{
			Success:   false,
			Message:   ae.Message,
			Errors:    []ErrDetail{{Code: ae.Code, Message: ae.Message, Details: ae.Details}},
			RequestID: rid,
		})
		return
	}

	c.JSON(http.StatusInternalServerError, Envelope{
		Success:   false,
		Message:   "internal server error",
		Errors:    []ErrDetail{{Code: "internal_error", Message: "internal server error"}},
		RequestID: rid,
	})
}

type pagination struct {
	Page       int  `json:"page"`
	Size       int  `json:"size"`
	Total      int  `json:"total"`
	TotalPages int  `json:"total_pages"`
	HasNext    bool `json:"has_next"`
	HasPrev    bool `json:"has_prev"`
}

// Paginated writes a 200 envelope with items plus pagination metadata under Data.
func Paginated(c *gin.Context, items any, page, size, total int) {
	totalPages := 0
	if size > 0 {
		totalPages = (total + size - 1) / size
	}
	c.JSON(http.StatusOK, Envelope{
		Success: true,
		Message: "ok",
		Data: gin.H{
			"items": items,
			"pagination": pagination{
				Page:       page,
				Size:       size,
				Total:      total,
				TotalPages: totalPages,
				HasNext:    page < totalPages,
				HasPrev:    page > 1,
			},
		},
		RequestID: requestID(c),
	})
}

// ErrorHandler renders any error pushed onto gin's context (c.Error(...)) through
// the envelope, unless a response was already written. Lives here, not in errs,
// because errs must stay import-cycle-free (response already imports errs).
func ErrorHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		if len(c.Errors) > 0 && !c.Writer.Written() {
			Error(c, c.Errors.Last().Err)
		}
	}
}
