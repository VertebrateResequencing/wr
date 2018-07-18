package infoblox

import (
	"fmt"
)

// Error wraps a single Infoblox error
type Error map[string]interface{}

// Message returns the message of the Infoblox error
func (e Error) Message() string {
	return e["Error"].(string)
}

// Code returns code of the Infoblox error
func (e Error) Code() string {
	return e["code"].(string)
}

// Text returns the text of the Infoblox error
func (e Error) Text() string {
	return e["text"].(string)
}

// Error returns a formatted string containing all of the error details
func (e Error) Error() string {
	return fmt.Sprintf("Error %s - %s - %s", e.Message(), e.Code(), e.Text())
}

// Errors wraps multiple Infoblox errors
type Errors map[string]interface{}

func (e Errors) Error() string {
	var (
		msg string
		err Error
		ok  bool
	)
	for _, val := range e["errors"].([]interface{}) {
		if err, ok = val.(map[string]interface{}); ok {
			msg += err.Error() + ". "
		}
	}
	return msg
}

func (e Errors) String() string {
	return e.Error()
}

// Errors returns a slice of Error
func (e Errors) Errors() []Error {
	var errs = e["errors"].([]interface{})
	var out = make([]Error, len(errs))
	for i, val := range errs {
		out[i] = Error(val.(map[string]interface{}))
	}
	return out
}
