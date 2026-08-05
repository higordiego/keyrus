package ledgerv1

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
	LedgerService_CreateEntry_FullMethodName    = "/cashflow.ledger.public.v1.LedgerService/CreateEntry"
	LedgerService_GetEntry_FullMethodName       = "/cashflow.ledger.public.v1.LedgerService/GetEntry"
	LedgerService_ListEntries_FullMethodName    = "/cashflow.ledger.public.v1.LedgerService/ListEntries"
	LedgerService_CreateReversal_FullMethodName = "/cashflow.ledger.public.v1.LedgerService/CreateReversal"
)

// LedgerServiceClient is the client API for LedgerService service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
//
// LedgerService exposes the public contract consumed through the edge HTTP adapter.
type LedgerServiceClient interface {
	CreateEntry(ctx context.Context, in *CreateEntryRequest, opts ...grpc.CallOption) (*CreateEntryResponse, error)
	GetEntry(ctx context.Context, in *GetEntryRequest, opts ...grpc.CallOption) (*GetEntryResponse, error)
	ListEntries(ctx context.Context, in *ListEntriesRequest, opts ...grpc.CallOption) (*ListEntriesResponse, error)
	CreateReversal(ctx context.Context, in *CreateReversalRequest, opts ...grpc.CallOption) (*CreateReversalResponse, error)
}

type ledgerServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewLedgerServiceClient(cc grpc.ClientConnInterface) LedgerServiceClient {
	return &ledgerServiceClient{cc}
}

func (c *ledgerServiceClient) CreateEntry(ctx context.Context, in *CreateEntryRequest, opts ...grpc.CallOption) (*CreateEntryResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(CreateEntryResponse)
	err := c.cc.Invoke(ctx, LedgerService_CreateEntry_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *ledgerServiceClient) GetEntry(ctx context.Context, in *GetEntryRequest, opts ...grpc.CallOption) (*GetEntryResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(GetEntryResponse)
	err := c.cc.Invoke(ctx, LedgerService_GetEntry_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *ledgerServiceClient) ListEntries(ctx context.Context, in *ListEntriesRequest, opts ...grpc.CallOption) (*ListEntriesResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ListEntriesResponse)
	err := c.cc.Invoke(ctx, LedgerService_ListEntries_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *ledgerServiceClient) CreateReversal(ctx context.Context, in *CreateReversalRequest, opts ...grpc.CallOption) (*CreateReversalResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(CreateReversalResponse)
	err := c.cc.Invoke(ctx, LedgerService_CreateReversal_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// LedgerServiceServer is the server API for LedgerService service.
// All implementations must embed UnimplementedLedgerServiceServer
// for forward compatibility.
//
// LedgerService exposes the public contract consumed through the edge HTTP adapter.
type LedgerServiceServer interface {
	CreateEntry(context.Context, *CreateEntryRequest) (*CreateEntryResponse, error)
	GetEntry(context.Context, *GetEntryRequest) (*GetEntryResponse, error)
	ListEntries(context.Context, *ListEntriesRequest) (*ListEntriesResponse, error)
	CreateReversal(context.Context, *CreateReversalRequest) (*CreateReversalResponse, error)
	mustEmbedUnimplementedLedgerServiceServer()
}

// UnimplementedLedgerServiceServer must be embedded to have
// forward compatible implementations.
//
// NOTE: this should be embedded by value instead of pointer to avoid a nil
// pointer dereference when methods are called.
type UnimplementedLedgerServiceServer struct{}

func (UnimplementedLedgerServiceServer) CreateEntry(context.Context, *CreateEntryRequest) (*CreateEntryResponse, error) {
	return nil, status.Error(codes.Unimplemented, "method CreateEntry not implemented")
}
func (UnimplementedLedgerServiceServer) GetEntry(context.Context, *GetEntryRequest) (*GetEntryResponse, error) {
	return nil, status.Error(codes.Unimplemented, "method GetEntry not implemented")
}
func (UnimplementedLedgerServiceServer) ListEntries(context.Context, *ListEntriesRequest) (*ListEntriesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "method ListEntries not implemented")
}
func (UnimplementedLedgerServiceServer) CreateReversal(context.Context, *CreateReversalRequest) (*CreateReversalResponse, error) {
	return nil, status.Error(codes.Unimplemented, "method CreateReversal not implemented")
}
func (UnimplementedLedgerServiceServer) mustEmbedUnimplementedLedgerServiceServer() {}
func (UnimplementedLedgerServiceServer) testEmbeddedByValue()                       {}

// UnsafeLedgerServiceServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to LedgerServiceServer will
// result in compilation errors.
type UnsafeLedgerServiceServer interface {
	mustEmbedUnimplementedLedgerServiceServer()
}

func RegisterLedgerServiceServer(s grpc.ServiceRegistrar, srv LedgerServiceServer) {

	if t, ok := srv.(interface{ testEmbeddedByValue() }); ok {
		t.testEmbeddedByValue()
	}
	s.RegisterService(&LedgerService_ServiceDesc, srv)
}

func _LedgerService_CreateEntry_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(CreateEntryRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(LedgerServiceServer).CreateEntry(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: LedgerService_CreateEntry_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(LedgerServiceServer).CreateEntry(ctx, req.(*CreateEntryRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _LedgerService_GetEntry_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetEntryRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(LedgerServiceServer).GetEntry(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: LedgerService_GetEntry_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(LedgerServiceServer).GetEntry(ctx, req.(*GetEntryRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _LedgerService_ListEntries_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ListEntriesRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(LedgerServiceServer).ListEntries(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: LedgerService_ListEntries_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(LedgerServiceServer).ListEntries(ctx, req.(*ListEntriesRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _LedgerService_CreateReversal_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(CreateReversalRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(LedgerServiceServer).CreateReversal(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: LedgerService_CreateReversal_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(LedgerServiceServer).CreateReversal(ctx, req.(*CreateReversalRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// LedgerService_ServiceDesc is the grpc.ServiceDesc for LedgerService service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var LedgerService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "cashflow.ledger.public.v1.LedgerService",
	HandlerType: (*LedgerServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "CreateEntry",
			Handler:    _LedgerService_CreateEntry_Handler,
		},
		{
			MethodName: "GetEntry",
			Handler:    _LedgerService_GetEntry_Handler,
		},
		{
			MethodName: "ListEntries",
			Handler:    _LedgerService_ListEntries_Handler,
		},
		{
			MethodName: "CreateReversal",
			Handler:    _LedgerService_CreateReversal_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "cashflow/ledger/public/v1/ledger.proto",
}
