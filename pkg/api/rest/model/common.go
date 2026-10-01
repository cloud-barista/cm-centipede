package model

import (
	"strconv"

	"github.com/labstack/echo/v4"
)

// ApiResponse is the generic envelope returned by every endpoint.
//
// Data has no omitempty so that an empty list still serializes as "data": [].
type ApiResponse[T any] struct {
	Success bool   `json:"success"`
	Data    T      `json:"data"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ListResponse wraps paginated list results.
type ListResponse[T any] struct {
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"pageSize"`
	Items    []T   `json:"items"`
}

// SimpleMessageResponse carries a human-readable message in the data field.
type SimpleMessageResponse struct {
	Message string `json:"message"`
}

// SuccessResponse wraps data in a successful ApiResponse.
func SuccessResponse[T any](data T) ApiResponse[T] {
	return ApiResponse[T]{Success: true, Data: data}
}

// SuccessResponseWithMessage wraps data and attaches a message.
func SuccessResponseWithMessage[T any](data T, msg string) ApiResponse[T] {
	return ApiResponse[T]{Success: true, Data: data, Message: msg}
}

// SimpleErrorResponse returns an error ApiResponse with no data.
func SimpleErrorResponse(msg string) ApiResponse[any] {
	return ApiResponse[any]{Success: false, Error: msg}
}

// ParsePageParams reads `page` and `pageSize` query parameters.
// Defaults: page=1, pageSize=20. pageSize is capped at 100.
func ParsePageParams(c echo.Context) (page, pageSize int) {
	page = 1
	pageSize = 20

	if p, err := strconv.Atoi(c.QueryParam("page")); err == nil && p > 0 {
		page = p
	}
	if ps, err := strconv.Atoi(c.QueryParam("pageSize")); err == nil && ps > 0 {
		if ps > 100 {
			ps = 100
		}
		pageSize = ps
	}
	return
}
