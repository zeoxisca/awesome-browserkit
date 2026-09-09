package browserkit

import "fmt"

// Error 是调用方可识别的结构化错误。
type Error struct {
	Kind                string `json:"kind"`
	Message             string `json:"message"`
	Recoverable         bool   `json:"recoverable"`
	RecommendedNextStep string `json:"recommended_next_step,omitempty"`
}

// Error 返回适合日志和普通错误链的简短错误。
func (err *Error) Error() string {
	if err == nil {
		return ""
	}
	if err.Kind == "" {
		return err.Message
	}
	return fmt.Sprintf("%s: %s", err.Kind, err.Message)
}

func newError(kind, message, next string, recoverable bool) error {
	return &Error{Kind: kind, Message: message, RecommendedNextStep: next, Recoverable: recoverable}
}
