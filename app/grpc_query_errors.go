package app

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	gogogrpc "github.com/cosmos/gogoproto/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// RegisterGRPCServerWithSkipCheckHeader overrides the embedded BaseApp method
// (the entrypoint the SDK's NewGRPCServer uses) to wrap the server so query
// errors carry the same status codes the ABCI query path produces.
//
// On the ABCI path, baseapp wraps handler errors carrying no explicit gRPC
// status as ErrInvalidRequest, and the gateway client maps ABCI codes back to
// gRPC codes (18→InvalidArgument, 4→Unauthenticated, 22→NotFound), so plain
// querier errors like "trading is halted" surface as HTTP 400. On the direct
// gRPC path the same errors would surface as codes.Unknown → HTTP 500. Without
// this shim, routing REST via gRPC (api.query-via-grpc) turns ordinary client
// errors into 500s.
func (app *THORChainApp) RegisterGRPCServerWithSkipCheckHeader(server gogogrpc.Server, skipCheckHeader bool) {
	app.BaseApp.RegisterGRPCServerWithSkipCheckHeader(abciParityServer{server}, skipCheckHeader)
}

type abciParityServer struct {
	gogogrpc.Server
}

func (s abciParityServer) RegisterService(sd *grpc.ServiceDesc, handler interface{}) {
	wrapped := *sd
	wrapped.Methods = make([]grpc.MethodDesc, len(sd.Methods))
	for i, method := range sd.Methods {
		inner := method.Handler
		wrapped.Methods[i] = grpc.MethodDesc{
			MethodName: method.MethodName,
			Handler: func(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
				resp, err := inner(srv, ctx, dec, interceptor)
				return resp, abciParityError(err)
			},
		}
	}
	s.Server.RegisterService(&wrapped, handler)
}

// abciParityError mirrors the ABCI query path's error translation (baseapp's
// gRPCErrorToSDKError composed with the gateway client's sdkErrorToGRPCError)
// so both routings yield identical REST status codes and bodies.
func abciParityError(err error) error {
	if err == nil {
		return nil
	}
	// explicit gRPC status set by a handler (or codes.Internal from the
	// recovery interceptor) — respect it; sdk errors registered without a gRPC
	// code report Unknown and are resolved by ABCI code below
	if s, ok := grpcstatus.FromError(err); ok && s.Code() != codes.Unknown {
		return err
	}
	space, code, log := errorsmod.ABCIInfo(err, false)
	if space == errorsmod.UndefinedCodespace {
		// unregistered error ("trading is halted", bad quote params, ...): the
		// ABCI path wraps these as ErrInvalidRequest → HTTP 400; ABCIInfo
		// redacts their message, so rebuild it the way that path renders it
		return grpcstatus.Error(codes.InvalidArgument, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, err.Error()).Error())
	}
	switch code {
	case sdkerrors.ErrInvalidRequest.ABCICode():
		return grpcstatus.Error(codes.InvalidArgument, log)
	case sdkerrors.ErrUnauthorized.ABCICode():
		return grpcstatus.Error(codes.Unauthenticated, log)
	case sdkerrors.ErrKeyNotFound.ABCICode():
		return grpcstatus.Error(codes.NotFound, log)
	default:
		return grpcstatus.Error(codes.Unknown, log)
	}
}
