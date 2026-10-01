package controller

import (
	"net/http"

	"github.com/cloud-barista/cm-centipede/pkg/api/rest/model"
	"github.com/labstack/echo/v4"
)

// IsReady is set to true after all initialisation steps complete.
// The /readyz handler returns 503 until this flag is set.
var IsReady bool

// ReadyzResponse is the data payload of the /readyz response.
type ReadyzResponse struct {
	Status string `json:"status"`
}

// Readyz godoc
//
//	@Summary	Server readiness check
//	@Tags		utility
//	@Produce	json
//	@Success	200	{object}	model.ApiResponse[ReadyzResponse]
//	@Failure	503	{object}	model.ApiResponse[any]
//	@Router		/centipede/readyz [get]
func Readyz(c echo.Context) error {
	if !IsReady {
		return c.JSON(http.StatusServiceUnavailable, model.SimpleErrorResponse("Server is not ready"))
	}
	return c.JSON(http.StatusOK, model.SuccessResponse(ReadyzResponse{Status: "ok"}))
}
