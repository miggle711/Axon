// engine/api/types.go
package api

type ErrorResponse struct {
	Error string `json:"error"`
	Code  int    `json:"code"`
}

type SuccessResponse struct {
	Message string `json:"message"`
}
