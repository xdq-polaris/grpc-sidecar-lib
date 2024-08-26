package pkg

import (
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/golang/protobuf/proto"
	openAPIV3Proto "github.com/google/gnostic/openapiv3"
	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/dynamic"
	"github.com/jhump/protoreflect/grpcreflect"
	"github.com/pkg/errors"
	"github.com/swaggest/openapi-go"
	"github.com/swaggest/openapi-go/openapi3"
	"github.com/xdq-polaris/grpc-sidecar-lib/config"
	"github.com/xdq-polaris/grpc-sidecar-lib/pkg/http_rule"
	"github.com/xdq-polaris/grpc-sidecar-lib/static"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/types/descriptorpb"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

func ApplyHttpEngineFromConfig(engine gin.IRouter, sidecarConfig *config.SidecarConfig) {
	var kacp = keepalive.ClientParameters{
		Time:                30 * time.Second, // send pings every 10 seconds if there is no activity
		Timeout:             10 * time.Second, // wait 1 second for ping back
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
	var reflector = openapi3.NewReflector()
	var specDesc = svcDesc.GetName()
	reflector.Spec = &openapi3.Spec{
		Openapi: "3.0.3",
		Info: openapi3.Info{
			Title:       svcDesc.GetName(),
			Description: &specDesc,
			Version:     "1.0.0",
		},
	}
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
		//swagger op
		if err := addOpenAPIOperation(httpMethod, httpPath,
			reflector,
			method); err != nil {
			panic(errors.Wrap(err, "add openapi operation"))
		}
		//service handler
		engine.Handle(httpMethod, httpPath,
			newServiceHandler(serviceClientConn, method, allowedRequestHeaders))
	}
	apiDocData, err := reflector.Spec.MarshalYAML()
	if err != nil {
		panic(errors.Wrap(err, "marshal openapi spec to yaml"))
	}
	//var apiDocStr = string(apiDocData)
	var openapiHttpPath = fmt.Sprintf("/openapi/%s.yaml", svcDesc.GetName())
	engine.GET(openapiHttpPath, func(c *gin.Context) {
		c.Data(http.StatusOK, "text/yaml", apiDocData)
	})
	var swaggerUIHttpPath = fmt.Sprintf("/swagger-ui/%s.html", svcDesc.GetName())
	engine.GET(swaggerUIHttpPath, func(c *gin.Context) {
		var encodedApiDocStr = url.PathEscape(string(apiDocData))
		var dataUrl = fmt.Sprintf("data:text/plain,%s", encodedApiDocStr)
		htmlStr, err := static.GenerateFullHtml(dataUrl)
		if err != nil {
			var internalErr = errors.Wrap(err, "generate full html")
			c.String(http.StatusInternalServerError, internalErr.Error())
			return
		}
		c.Data(http.StatusOK, "text/html", []byte(htmlStr))
	})
}

func addOpenAPIOperation(httpMethod string, httpPath string,
	reflector *openapi3.Reflector,
	method *desc.MethodDescriptor) error {
	const contentType = "application/json"
	opCtx, err := reflector.NewOperationContext(httpMethod, httpPath)
	if err != nil {
		return errors.Wrap(err, "new openapi operation")
	}
	//apply api document
	var options = method.GetOptions()
	operationExtObj, err := proto.GetExtension(options, openAPIV3Proto.E_Operation)
	if err != nil {
		var internalErr = errors.Wrap(err, "get operation proto extension")
		fmt.Println("[openapi] skip generate operation:", method.GetFullyQualifiedName(), "err:", internalErr)
	} else {
		var operationExt = operationExtObj.(*openAPIV3Proto.Operation)
		opCtx.SetDescription(operationExt.Description)
	}
	reqTemplateType, err := protoMessageToGoStruct(method.GetInputType())
	if err != nil {
		return errors.Wrap(err, "convert grpc request msg")
	}
	var reqTemplate = reflect.New(reqTemplateType).Elem().Interface()
	opCtx.AddReqStructure(reqTemplate,
		openapi.WithContentType(contentType))
	respTemplateType, err := protoMessageToGoStruct(method.GetOutputType())
	if err != nil {
		return errors.Wrap(err, "convert grpc response msg")
	}
	var respTemplate = reflect.New(respTemplateType).Elem().Interface()
	opCtx.AddRespStructure(respTemplate,
		openapi.WithContentType(contentType))
	if err := reflector.AddOperation(opCtx); err != nil {
		return errors.Wrap(err, "add operation to openapi reflector")
	}
	return nil
}

func protoMessageToGoStruct(messageDesc *desc.MessageDescriptor) (reflect.Type, error) {
	var structFields = make([]reflect.StructField, 0)
	var protoFields = messageDesc.GetFields()
	var dynamicMsg = dynamic.NewMessage(messageDesc)
	for _, protoField := range protoFields {
		var protoFieldName = protoField.GetName()
		var dynamicFieldValue = dynamicMsg.GetField(protoField)
		var fieldProtoType = protoField.GetType()
		var fieldReflectType = reflect.Type(nil)
		switch fieldProtoType {
		case descriptorpb.FieldDescriptorProto_TYPE_MESSAGE:
			msgType, err := protoMessageToGoStruct(protoField.GetMessageType())
			if err != nil {
				return nil, errors.Wrapf(err, "convert message type for field:%s.%s",
					messageDesc.GetName(), protoFieldName)
			}
			fieldReflectType = msgType
			break

		//case descriptorpb.FieldDescriptorProto_TYPE_ENUM:
		//	fieldReflectType=reflect.TypeOf(int(0))
		//	break
		default:
			fieldReflectType = reflect.TypeOf(dynamicFieldValue)
		}
		var fieldTagStr = ""
		//先检查有没有openapi option
		var fieldOptions = protoField.GetOptions()
		propertyExtObj, err := proto.GetExtension(fieldOptions, openAPIV3Proto.E_Property)
		if err != nil {
			var internalErr = errors.Wrap(err, "get schema proto extension")
			fmt.Println("[openapi] skip generate schema:", protoField.GetFullyQualifiedName(), "err:", internalErr)
		} else {
			var schemaExt = propertyExtObj.(*openAPIV3Proto.Schema)
			fieldTagStr = fmt.Sprintf(`%s title:"%s" description:"%s"`,
				fieldTagStr, schemaExt.Title, schemaExt.Description)
		}
		//处理repeated和optional这种label
		var fieldProtoLabel = protoField.GetLabel()
		switch fieldProtoLabel {
		case descriptorpb.FieldDescriptorProto_LABEL_REPEATED:
			fieldReflectType = reflect.SliceOf(fieldReflectType)
		}
		fieldTagStr = fmt.Sprintf(`%s json:"%s"`, fieldTagStr, protoField.GetName())
		var reflectFieldName = strings.ToUpper(string(protoFieldName[0])) + protoFieldName[1:]
		var structField = reflect.StructField{
			Name: reflectFieldName,
			Type: fieldReflectType,
			Tag:  reflect.StructTag(fieldTagStr),
		}
		structFields = append(structFields, structField)
	}
	var reflectMsgType = reflect.StructOf(structFields)
	return reflectMsgType, nil
}

func unicodeEncode(str string) string {
	textQuoted := strconv.QuoteToASCII(str)
	textUnquoted := textQuoted[1 : len(textQuoted)-1]
	return textUnquoted
}
