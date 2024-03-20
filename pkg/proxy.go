package pkg

import (
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/golang/protobuf/proto"
	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/grpcreflect"
	"github.com/pkg/errors"
	"github.com/xdq-polaris/grpc-sidecar-lib/config"
	"github.com/xdq-polaris/grpc-sidecar-lib/pkg/http_rule"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"strings"
	"time"
)

func ApplyHttpEngineFromConfig(engine gin.IRouter, sidecarConfig *config.SidecarConfig) {
	var kacp = keepalive.ClientParameters{
		Time:                10 * time.Second, // send pings every 10 seconds if there is no activity
		Timeout:             time.Second,      // wait 1 second for ping back
		PermitWithoutStream: true,             // send pings even without active streams
	}
	// 连接到gRPC服务
	conn, err := grpc.Dial(sidecarConfig.BackendAddress,
		grpc.WithInsecure(),
		grpc.WithKeepaliveParams(kacp),
		grpc.WithBlock())
	if err != nil {
		panic(errors.Wrap(err, "dial grpc"))
	}

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
			serviceName, svcDesc, conn, allowedRequestHeaders)
	}
}

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
		httpMethod, httpPath := http_rule.HttpRulePatternToMethod(httpRule)
		fmt.Println(fmt.Sprintf("%s->%s", serviceName, methodName), httpMethod, httpPath)

		engine.Handle(httpMethod, httpPath,
			newServiceHandler(serviceClientConn, method, allowedRequestHeaders))
	}
}
