package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	payment_v1 "shared/pkg/proto/payment/v1"
	"syscall"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

const grpcPort = 5052

type PaymentService struct {
	payment_v1.UnimplementedPaymentServiceServer
}

func (p *PaymentService) PayOrder(ctx context.Context, req *payment_v1.PayOrderRequest) (*payment_v1.PayOrderResponse, error) {
	if req.OrderUuid == "" {
		return nil, status.Error(codes.InvalidArgument, "order uuid is required")
	}
	if req.UserUuid == "" {
		return nil, status.Error(codes.InvalidArgument, "user uuid is required")
	}
	if req.PaymentMethod == payment_v1.PaymentMethod_PAYMENT_METHOD_UNSPECIFIED {
		return nil, status.Error(codes.InvalidArgument, "payment method is required")
	}

	transactionUUID := uuid.New().String()
	paymentMethodName := getPaymentMethod(req.PaymentMethod)
	log.Printf("Оплата прошла успешно, transaction_uuid: %s", transactionUUID)
	log.Printf("Детали оплаты: order_uuid=%s, user_uuid=%s, payment_method=%s",
		req.OrderUuid, req.UserUuid, paymentMethodName)

	return &payment_v1.PayOrderResponse{TransactionUuid: transactionUUID}, nil
}

func getPaymentMethod(method payment_v1.PaymentMethod) string {
	switch method {
	case payment_v1.PaymentMethod_PAYMENT_METHOD_CARD:
		return "CARD (Банковская карта)"
	case payment_v1.PaymentMethod_PAYMENT_METHOD_SBP:
		return "SBP (Система быстрых платежей)"
	case payment_v1.PaymentMethod_PAYMENT_METHOD_CREDIT_CARD:
		return "CREDIT_CARD (Кредитная карта)"
	case payment_v1.PaymentMethod_PAYMENT_METHOD_INVESTOR_MONEY:
		return "INVESTOR_MONEY (Деньги инвестора)"
	default:
		return "UNKNOWN"
	}
}

func main() {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", grpcPort))
	if err != nil {
		log.Printf("failed to listen: %v", err)
		return
	}
	defer func() {
		if cerr := lis.Close(); cerr != nil {
			log.Printf("failed to close listener: %v\n", cerr)
		}
	}()

	paymentGrpcServer := grpc.NewServer()
	service := &PaymentService{}

	payment_v1.RegisterPaymentServiceServer(paymentGrpcServer, service)

	reflection.Register(paymentGrpcServer)

	go func() {
		log.Printf("GRPC serever listening on %d\n", grpcPort)
		err = paymentGrpcServer.Serve(lis)
		if err != nil {
			log.Printf("failed to serve: %v\n", err)
			return
		}
	}()
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Printf("Shutdown Server ...")
	paymentGrpcServer.GracefulStop()
	log.Printf("Server stopped")
}
