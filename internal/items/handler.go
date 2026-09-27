package items

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prajwalmahajan101/busyapi/internal/errs"
	"github.com/prajwalmahajan101/busyapi/internal/response"
	storedb "github.com/prajwalmahajan101/busyapi/internal/store/db"
)

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) RegisterRoutes(r gin.IRouter) {
	g := r.Group("/items")
	g.POST("", h.create)
	g.GET("", h.list)
	g.GET("/:id", h.get)
	g.DELETE("/:id", h.softDelete)
	g.DELETE("/:id/hard", h.hardDelete)
}

// itemDTO is the wire shape. pgtype.Timestamptz does not marshal to clean JSON,
// so the store row is mapped explicitly.
type itemDTO struct {
	ID        int64           `json:"id"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	IsActive  bool            `json:"is_active"`
	Notes     json.RawMessage `json:"notes,omitempty"`
}

func toDTO(i storedb.Item) itemDTO {
	return itemDTO{
		ID:        i.ID,
		CreatedAt: i.CreatedAt.Time,
		UpdatedAt: i.UpdatedAt.Time,
		IsActive:  i.IsActive,
		Notes:     json.RawMessage(i.Notes),
	}
}

type createReq struct {
	Notes json.RawMessage `json:"notes"`
}

func (h *Handler) create(c *gin.Context) {
	var req createReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, errs.NewValidation("invalid request body", nil))
		return
	}
	item, err := h.svc.Create(c.Request.Context(), req.Notes)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.Success(c, http.StatusCreated, "item created", toDTO(item))
}

func (h *Handler) list(c *gin.Context) {
	page := queryInt(c, "page", 1)
	size := queryInt(c, "size", 20)

	items, total, err := h.svc.List(c.Request.Context(), page, size)
	if err != nil {
		response.Error(c, err)
		return
	}
	dtos := make([]itemDTO, len(items))
	for i, it := range items {
		dtos[i] = toDTO(it)
	}
	response.Paginated(c, dtos, page, size, int(total))
}

func (h *Handler) get(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		response.Error(c, err)
		return
	}
	item, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.Success(c, http.StatusOK, "ok", toDTO(item))
}

func (h *Handler) softDelete(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		response.Error(c, err)
		return
	}
	if err := h.svc.SoftDelete(c.Request.Context(), id); err != nil {
		response.Error(c, err)
		return
	}
	response.Success(c, http.StatusOK, "item soft deleted", nil)
}

func (h *Handler) hardDelete(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		response.Error(c, err)
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		response.Error(c, err)
		return
	}
	response.Success(c, http.StatusOK, "item deleted", nil)
}

func parseID(c *gin.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, errs.NewValidation("invalid id", nil)
	}
	return id, nil
}

func queryInt(c *gin.Context, key string, def int) int {
	v, err := strconv.Atoi(c.Query(key))
	if err != nil || v < 1 {
		return def
	}
	return v
}
