package http_rule

import (
	"github.com/pkg/errors"
	"google.golang.org/genproto/googleapis/api/annotations"
	"net/http"
)

func HttpRulePatternToMethod(httpRule *annotations.HttpRule) (method string, path string) {
	var patternObj = httpRule.Pattern
	switch patternObj.(type) {
	case *annotations.HttpRule_Get:
		var pattern = patternObj.(*annotations.HttpRule_Get)
		return http.MethodGet, pattern.Get
	case *annotations.HttpRule_Post:
		var pattern = patternObj.(*annotations.HttpRule_Post)
		return http.MethodPost, pattern.Post
	case *annotations.HttpRule_Put:
		var pattern = patternObj.(*annotations.HttpRule_Put)
		return http.MethodPut, pattern.Put
	case *annotations.HttpRule_Delete:
		var pattern = patternObj.(*annotations.HttpRule_Delete)
		return http.MethodDelete, pattern.Delete
	case *annotations.HttpRule_Patch:
		var pattern = patternObj.(*annotations.HttpRule_Patch)
		return http.MethodPatch, pattern.Patch
	case *annotations.HttpRule_Custom:
		var customRule = patternObj.(*annotations.HttpRule_Custom)
		return customRule.Custom.Kind, customRule.Custom.Path
	default:
		panic(errors.Errorf("unknown http rule pattern:%v", httpRule.Pattern))
	}
}
