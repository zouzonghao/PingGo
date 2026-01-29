package errors

import "net/http"

type AppError struct {
	Code       int    `json:"code"`
	Message    string `json:"message"`
	StatusCode int    `json:"-"`
	Err        error  `json:"-"`
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Message
}

func (e *AppError) Unwrap() error {
	return e.Err
}

// Predefined errors
var (
	ErrUnauthorized = &AppError{
		Code:       401,
		Message:    "Unauthorized",
		StatusCode: http.StatusUnauthorized,
	}
	ErrForbidden = &AppError{
		Code:       403,
		Message:    "Forbidden",
		StatusCode: http.StatusForbidden,
	}
	ErrNotFound = &AppError{
		Code:       404,
		Message:    "Resource not found",
		StatusCode: http.StatusNotFound,
	}
	ErrBadRequest = &AppError{
		Code:       400,
		Message:    "Bad request",
		StatusCode: http.StatusBadRequest,
	}
	ErrInternalServer = &AppError{
		Code:       500,
		Message:    "Internal server error",
		StatusCode: http.StatusInternalServerError,
	}
)

func New(code int, message string, statusCode int, err error) *AppError {
	return &AppError{
		Code:       code,
		Message:    message,
		StatusCode: statusCode,
		Err:        err,
	}
}

func Wrap(err error, message string) *AppError {
	return &AppError{
		Code:       500,
		Message:    message,
		StatusCode: http.StatusInternalServerError,
		Err:        err,
	}
}
