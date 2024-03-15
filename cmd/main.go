package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/golang/protobuf/proto"
	"github.com/jhump/protoreflect/dynamic"
	"github.com/pelletier/go-toml/v2"
	"github.com/samber/lo"
	"github.com/xdq-polaris/grpc-sidecar-lib/config"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc/metadata"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/dynamic/grpcdynamic"
	"github.com/jhump/protoreflect/grpcreflect"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
)

func doResolveService(engine gin.IRouter,
	serviceName string, svcDesc *desc.ServiceDescriptor, serviceClientConn *grpc.ClientConn,
	allowedRequestHeaders []string) {
	var methods = svcDesc.GetMethods()
	for _, method := range methods {
		var methodName = method.GetName()
		fmt.Println(methodName)
		if method.IsServerStreaming() || method.IsClientStreaming() {
			fmt.Println("skip method:", methodName, "streaming not supported")
			continue
		}
		var options = method.GetOptions()
		fmt.Println(options)
		//httpRule := &annotations.HttpRule{}
		httpRuleObj, err := proto.GetExtension(options, annotations.E_Http)
		if err != nil {
			var internalErr = errors.Wrap(err, "get http rule")
			fmt.Println("skip method:", methodName, "err:", internalErr)
			continue
		}
		httpRule, ok := httpRuleObj.(*annotations.HttpRule)
		if !ok {
			panic(errors.Wrap(err, "cast to HttpRule"))
		}
		httpMethod, httpPath := httpRulePatternToMethod(httpRule)
		fmt.Println(fmt.Sprintf("%s->%s", serviceName, methodName), httpMethod, httpPath)
		engine.Handle(httpMethod, httpPath,
			newServiceHandler(serviceClientConn, method, allowedRequestHeaders))
	}
}

func newServiceHandler(serviceClientConn *grpc.ClientConn, method *desc.MethodDescriptor,
	allowedRequestHeaders []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		r, w := c.Request, c.Writer
		requestMD := metadata.New(map[string]string{})
		for key, values := range r.Header {
			if !lo.Contains(allowedRequestHeaders, strings.ToLower(key)) {
				continue
			}
			for _, value := range values {
				requestMD.Append(key, value)
			}
		}
		var ctx = context.Background()
		ctx = metadata.NewOutgoingContext(ctx, requestMD)

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
		response, err := stub.InvokeRpc(ctx, method, dynamicMsg)
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
		w.Write(respJSON)
	}
}

func httpRulePatternToMethod(httpRule *annotations.HttpRule) (method string, path string) {
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

func NewHttpEngineFromConfig(sidecarConfig *config.SidecarConfig) *gin.Engine {
	// 连接到gRPC服务
	conn, err := grpc.Dial(sidecarConfig.BackendAddress, grpc.WithInsecure())
	if err != nil {
		panic(errors.Wrap(err, "dial grpc"))
	}
	defer conn.Close()
	var engine = gin.Default()

	// 使用gRPC reflection创建一个reflection client
	refClient := grpcreflect.NewClientAuto(context.Background(), conn)
	serviceNames, err := refClient.ListServices()
	if err != nil {
		panic(errors.Wrap(err, "list services"))
	}
	var allowedRequestHeaders = sidecarConfig.AllowRequestHeaders
	for _, serviceName := range serviceNames {
		fmt.Println(serviceName)
		if strings.HasPrefix(serviceName, "grpc.reflection") {
			fmt.Println("skip service:", serviceName)
			continue
		}
		// 从reflection获取服务描述符
		svcDesc, err := refClient.ResolveService(serviceName)
		if err != nil {
			panic(errors.Wrapf(err, "resolve service:%s", serviceName))
		}
		doResolveService(engine,
			serviceName, svcDesc, conn,
			allowedRequestHeaders)
	}
	return engine
}

func main() {
	var configPath = os.Getenv("CONFIG_PATH")
	configData, err := os.ReadFile(configPath)
	if err != nil {
		panic(errors.Wrap(err, "read config file"))
	}
	var sidecarConfig = &config.SidecarConfig{}
	if err := toml.Unmarshal(configData, sidecarConfig); err != nil {
		panic(errors.Wrap(err, "unmarshal config"))
	}
	var engine = NewHttpEngineFromConfig(sidecarConfig)
	// 启动HTTP服务器
	if err := engine.Run(fmt.Sprintf(":%d", sidecarConfig.Port)); err != nil {
		panic(err)
	}
}
