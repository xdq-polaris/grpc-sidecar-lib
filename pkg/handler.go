package pkg

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/dynamic"
	"github.com/jhump/protoreflect/dynamic/grpcdynamic"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"net/http"
)

type BeforeCallOptionFuncParam struct {
	ServiceName string
	MethodDesc  *desc.MethodDescriptor
	HttpContext *gin.Context
	RequestMsg  *dynamic.Message
}

type BeforeCallOptionFunc func(param *BeforeCallOptionFuncParam) (grpc.CallOption, error)

type AfterCallOptionFuncParam struct {
	ServiceName string
	MethodDesc  *desc.MethodDescriptor
	GinContext  *gin.Context
	ResponseMsg *dynamic.Message
}

type AfterCallOptionFunc func(param *AfterCallOptionFuncParam) (grpc.CallOption, error)

type ServiceHandlerOptions struct {
	BeforeCallOptionFuncs []BeforeCallOptionFunc
	AfterCallOptionFuncs  []AfterCallOptionFunc
}

func newServiceHandler(serviceClientConn *grpc.ClientConn, method *desc.MethodDescriptor,
	allowedRequestHeaders []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		r, w := c.Request, c.Writer
		requestMD := metadata.New(map[string]string{})
		var originHeader = c.Request.Header.Clone()
		c.Request.Header = make(http.Header)
		for _, headerKey := range allowedRequestHeaders {
			var values = originHeader.Values(headerKey)
			for _, value := range values {
				requestMD.Append(headerKey, value)
			}
		}
		var rpcRequestCtx = metadata.NewOutgoingContext(context.Background(), requestMD)

		// 解析HTTP请求体到动态消息
		dynamicMsg := dynamic.NewMessage(method.GetInputType())
		var fields = method.GetInputType().GetFields()
		if len(fields) > 0 {
			if err := json.NewDecoder(r.Body).Decode(dynamicMsg); err != nil {
				http.Error(w, fmt.Sprintf("Failed to decode request body: %v", err), http.StatusBadRequest)
				return
			}
		}

		// 使用grpcdynamic包执行gRPC请求
		stub := grpcdynamic.NewStub(serviceClientConn)
		response, err := stub.InvokeRpc(rpcRequestCtx, method, dynamicMsg)
		if err != nil {
			http.Error(w, fmt.Sprintf("gRPC call failed: %v", err), http.StatusInternalServerError)
			return
		}

		// 将响应转换回JSON并返回
		var responseMsg = dynamic.NewMessage(method.GetOutputType())
		if err := responseMsg.MergeFrom(response); err != nil {
			var internalErr = errors.Wrap(err, "merge response")
			http.Error(w, fmt.Sprintf("Failed to merge response: %v", internalErr),
				http.StatusInternalServerError)
			return
		}

		respJSON, err := responseMsg.MarshalJSON()
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to marshal response: %v", err),
				http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(respJSON); err != nil {
			http.Error(w, fmt.Sprintf("Failed to write response: %v", err),
				http.StatusInternalServerError)
			return
		}
	}
}
