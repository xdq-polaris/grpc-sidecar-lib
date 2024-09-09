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
	"github.com/xdq-polaris/grpc-sidecar-lib/utils"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"io"
	"net/http"
	"strings"
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
		//requestMD.Append("Connection", "keep-alive")
		//httpContext, cancelFunc := context.WithTimeout(c.Request.Context(), 1*time.Hour)
		//defer cancelFunc()
		var httpContext = context.Background()
		var rpcRequestCtx = metadata.NewOutgoingContext(httpContext, requestMD)

		// 解析HTTP请求体到动态消息
		dynamicMsg := dynamic.NewMessage(method.GetInputType())
		if r.Body == http.NoBody {
			var emptyJsonReader = strings.NewReader("{}")
			r.Body = io.NopCloser(emptyJsonReader)
		}
		if err := json.NewDecoder(r.Body).Decode(dynamicMsg); err != nil {
			http.Error(w, fmt.Sprintf("Failed to decode request body: %v", err), http.StatusBadRequest)
			return
		}

		// 使用grpcdynamic包执行gRPC请求
		stub := grpcdynamic.NewStub(serviceClientConn)
		response, err := stub.InvokeRpc(rpcRequestCtx, method, dynamicMsg)
		if err != nil {
			handleInvokeError(c, err)
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

func handleInvokeError(c *gin.Context, err error) {
	statusObj, ok := status.FromError(err)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code": "Unsorted",
			"msg":  err.Error(),
		})
		return
	}
	type StatusMirror struct {
		s *spb.Status
	}
	var statusMirror = utils.UnsafeCast[*status.Status, *StatusMirror](statusObj)
	c.JSON(http.StatusInternalServerError, gin.H{
		"code":    statusObj.Code().String(),
		"msg":     statusObj.Message(),
		"details": statusMirror.s.Details,
	})
}
