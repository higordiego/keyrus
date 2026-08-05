package ledgerinternalv1

import (
	context "context"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
// Requires gRPC-Go v1.64.0 or later.
const _ = grpc.SupportPackageIsVersion9

const (
	LedgerInternalService_GetMerchantWatermark_FullMethodName = "/cashflow.ledger.internal.v1.LedgerInternalService/GetMerchantWatermark"
	LedgerInternalService_StreamEntriesAtCut_FullMethodName   = "/cashflow.ledger.internal.v1.LedgerInternalService/StreamEntriesAtCut"
)

// LedgerInternalServiceClient is the client API for LedgerInternalService service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
//
// LedgerInternalService is private gRPC. It must not be exposed through grpc-gateway.
type LedgerInternalServiceClient interface {
	GetMerchantWatermark(ctx context.Context, in *GetMerchantWatermarkRequest, opts ...grpc.CallOption) (*GetMerchantWatermarkResponse, error)
	StreamEntriesAtCut(ctx context.Context, in *StreamEntriesAtCutRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[StreamEntriesAtCutResponse], error)
}

type ledgerInternalServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewLedgerInternalServiceClient(cc grpc.ClientConnInterface) LedgerInternalServiceClient {
	return &ledgerInternalServiceClient{cc}
}

func (c *ledgerInternalServiceClient) GetMerchantWatermark(ctx context.Context, in *GetMerchantWatermarkRequest, opts ...grpc.CallOption) (*GetMerchantWatermarkResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(GetMerchantWatermarkResponse)
	err := c.cc.Invoke(ctx, LedgerInternalService_GetMerchantWatermark_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *ledgerInternalServiceClient) StreamEntriesAtCut(ctx context.Context, in *StreamEntriesAtCutRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[StreamEntriesAtCutResponse], error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	stream, err := c.cc.NewStream(ctx, &LedgerInternalService_ServiceDesc.Streams[0], LedgerInternalService_StreamEntriesAtCut_FullMethodName, cOpts...)
	if err != nil {
		return nil, err
	}
	x := &grpc.GenericClientStream[StreamEntriesAtCutRequest, StreamEntriesAtCutResponse]{ClientStream: stream}
	if err := x.ClientStream.SendMsg(in); err != nil {
		return nil, err
	}
	if err := x.ClientStream.CloseSend(); err != nil {
		return nil, err
	}
	return x, nil
}

// This type alias is provided for backwards compatibility with existing code that references the prior non-generic stream type by name.
type LedgerInternalService_StreamEntriesAtCutClient = grpc.ServerStreamingClient[StreamEntriesAtCutResponse]

// LedgerInternalServiceServer is the server API for LedgerInternalService service.
// All implementations must embed UnimplementedLedgerInternalServiceServer
// for forward compatibility.
//
// LedgerInternalService is private gRPC. It must not be exposed through grpc-gateway.
type LedgerInternalServiceServer interface {
	GetMerchantWatermark(context.Context, *GetMerchantWatermarkRequest) (*GetMerchantWatermarkResponse, error)
	StreamEntriesAtCut(*StreamEntriesAtCutRequest, grpc.ServerStreamingServer[StreamEntriesAtCutResponse]) error
	mustEmbedUnimplementedLedgerInternalServiceServer()
}

// UnimplementedLedgerInternalServiceServer must be embedded to have
// forward compatible implementations.
//
// NOTE: this should be embedded by value instead of pointer to avoid a nil
// pointer dereference when methods are called.
type UnimplementedLedgerInternalServiceServer struct{}

func (UnimplementedLedgerInternalServiceServer) GetMerchantWatermark(context.Context, *GetMerchantWatermarkRequest) (*GetMerchantWatermarkResponse, error) {
	return nil, status.Error(codes.Unimplemented, "method GetMerchantWatermark not implemented")
}
func (UnimplementedLedgerInternalServiceServer) StreamEntriesAtCut(*StreamEntriesAtCutRequest, grpc.ServerStreamingServer[StreamEntriesAtCutResponse]) error {
	return status.Error(codes.Unimplemented, "method StreamEntriesAtCut not implemented")
}
func (UnimplementedLedgerInternalServiceServer) mustEmbedUnimplementedLedgerInternalServiceServer() {}
func (UnimplementedLedgerInternalServiceServer) testEmbeddedByValue()                               {}

// UnsafeLedgerInternalServiceServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to LedgerInternalServiceServer will
// result in compilation errors.
type UnsafeLedgerInternalServiceServer interface {
	mustEmbedUnimplementedLedgerInternalServiceServer()
}

func RegisterLedgerInternalServiceServer(s grpc.ServiceRegistrar, srv LedgerInternalServiceServer) {

	if t, ok := srv.(interface{ testEmbeddedByValue() }); ok {
		t.testEmbeddedByValue()
	}
	s.RegisterService(&LedgerInternalService_ServiceDesc, srv)
}

func _LedgerInternalService_GetMerchantWatermark_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetMerchantWatermarkRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(LedgerInternalServiceServer).GetMerchantWatermark(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: LedgerInternalService_GetMerchantWatermark_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(LedgerInternalServiceServer).GetMerchantWatermark(ctx, req.(*GetMerchantWatermarkRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _LedgerInternalService_StreamEntriesAtCut_Handler(srv interface{}, stream grpc.ServerStream) error {
	m := new(StreamEntriesAtCutRequest)
	if err := stream.RecvMsg(m); err != nil {
		return err
	}
	return srv.(LedgerInternalServiceServer).StreamEntriesAtCut(m, &grpc.GenericServerStream[StreamEntriesAtCutRequest, StreamEntriesAtCutResponse]{ServerStream: stream})
}

// This type alias is provided for backwards compatibility with existing code that references the prior non-generic stream type by name.
type LedgerInternalService_StreamEntriesAtCutServer = grpc.ServerStreamingServer[StreamEntriesAtCutResponse]

// LedgerInternalService_ServiceDesc is the grpc.ServiceDesc for LedgerInternalService service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var LedgerInternalService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "cashflow.ledger.internal.v1.LedgerInternalService",
	HandlerType: (*LedgerInternalServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "GetMerchantWatermark",
			Handler:    _LedgerInternalService_GetMerchantWatermark_Handler,
		},
	},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "StreamEntriesAtCut",
			Handler:       _LedgerInternalService_StreamEntriesAtCut_Handler,
			ServerStreams: true,
		},
	},
	Metadata: "cashflow/ledger/internal/v1/ledger_internal.proto",
}
