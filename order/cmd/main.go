package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	inventoryv1 "shared/pkg/proto/inventory/v1"
	paymentv1 "shared/pkg/proto/payment/v1"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"google.golang.org/grpc"
)

type PaymentMethod string

const (
	PaymentMethodUnknown       PaymentMethod = "UNKNOWN"
	PaumentMethodCard          PaymentMethod = "CARD"
	PaymentMethodSbp           PaymentMethod = "SBP"
	PaymentMethodCreditCard    PaymentMethod = "CREDIT_CARD"
	PaymentMethodInvestorMoney PaymentMethod = "INVESTOR_MONEY"
)

type OrderStatus string

const (
	OrderStatusPendingPayment OrderStatus = "PENDING_PAYMENT"
	OrderStatusPaid           OrderStatus = "PAID"
	OrderStatusCancelled      OrderStatus = "CANCELLED"
	OrderStatusFailed         OrderStatus = "FAILED"
)

type Order struct {
	OrderUuid       string        `json:"order_uuid"`                 //Уникальный идентификатор заказа
	UserUuid        string        `json:"user_uuid"`                  //UUID пользователя
	PartsUuid       []string      `json:"parts_uuid"`                 //Список UUID деталей
	TotalPrice      float64       `json:"total_price"`                //Итоговая стоимость
	TransactionUuid *string       `json:"transaction_uuid,omitempty"` //UUID транзакции (если оплачен)
	PaymentMethod   PaymentMethod `json:"payment_method,omitempty"`   //Способ оплаты (если оплачен)
	Status          OrderStatus   `json:"status"`                     //Статус (`PENDING_PAYMENT`, `PAID`, `CANCELLED`)
}

const (
	httpPort             = "8080"
	readHeaderTimeout    = 5 * time.Second
	shutdownTimeout      = 10 * time.Second
	inventoryServiceAddr = "localhost:50051"
)

type OrderStorage struct {
	mu     sync.RWMutex
	orders map[string]*Order
}

func NewOrderStorage() *OrderStorage {
	return &OrderStorage{
		orders: make(map[string]*Order),
	}
}

// CreateOrderRequest - запрос на создание заказа
type CreateOrderRequest struct {
	UserUUID  string   `json:"user_uuid"`
	PartUUIDs []string `json:"part_uuids"`
}

// CreateOrderResponse - ответ при создании заказа
type CreateOrderResponse struct {
	OrderUUID  string  `json:"order_uuid"`
	TotalPrice float64 `json:"total_price"`
}

func (s *Server) CreateOrder(w http.ResponseWriter, r *http.Request) {
	var req CreateOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.UserUUID == "" || len(req.PartUUIDs) == 0 {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	inventoryReq := inventoryv1.ListPartsRequest{
		Filter: &inventoryv1.PartsFilter{
			Uuids: req.PartUUIDs,
		},
	}
	inventoryResp, err := s.inventoryClient.ListParts(context.Background(), &inventoryReq)
	if err != nil {
		http.Error(w, "Failed to parts from InventoryService", http.StatusInternalServerError)
		return
	}

	if len(req.PartUUIDs) != len(inventoryResp.Parts) {
		http.Error(w, "Some parts not found", http.StatusNotFound)
		return
	}

	var totalPrice float64

	for _, part := range inventoryResp.Parts {
		totalPrice += part.Price
	}

	order := &Order{
		OrderUuid:  uuid.New().String(),
		UserUuid:   req.UserUUID,
		PartsUuid:  req.PartUUIDs,
		TotalPrice: totalPrice,
		Status:     OrderStatusPendingPayment,
	}
	s.storage.mu.Lock()
	s.storage.orders[order.OrderUuid] = order
	s.storage.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(CreateOrderResponse{
		OrderUUID:  order.OrderUuid,
		TotalPrice: order.TotalPrice,
	})
}

func (s *Server) PayOrder(w http.ResponseWriter, r *http.Request) {
	//TODO
}

func (s *Server) GetOrder(w http.ResponseWriter, r *http.Request) {
	orderUUID := chi.URLParam(r, "order_uuid")
	if orderUUID == "" {
		http.Error(w, "Order UUID is required", http.StatusBadRequest)
		return
	}
	s.storage.mu.RLock()
	order, ok := s.storage.orders[orderUUID]
	s.storage.mu.RUnlock()
	if !ok {
		http.Error(w, "Order not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(order)
}

func (s *Server) CancelOrder(w http.ResponseWriter, r *http.Request) {
	orderUUID := chi.URLParam(r, "order_uuid")
	if orderUUID == "" {
		http.Error(w, "Order UUID is required", http.StatusBadRequest)
		return
	}
	s.storage.mu.Lock()
	defer s.storage.mu.Unlock()
	order, ok := s.storage.orders[orderUUID]
	if !ok {
		http.Error(w, "Order not found", http.StatusNotFound)
		return
	}
	if order.Status == OrderStatusPaid {
		http.Error(w, "Order is paid and cannot be canceled", http.StatusConflict)
		return
	}
	if order.Status == OrderStatusPendingPayment {
		order.Status = OrderStatusCancelled
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Error(w, fmt.Sprintf("Order cannot be canceled in status: %s", order.Status), http.StatusConflict)
}

type Server struct {
	address         string
	storage         *OrderStorage
	inventoryClient inventoryv1.InventoryServiceClient
}

func main() {
	storage := NewOrderStorage()

	// 2. Подключаемся к inventory сервису (твой код из inventory/)
	inventoryConn, err := grpc.Dial(inventoryServiceAddr, grpc.WithInsecure())
	if err != nil {
		log.Fatalf("Failed to connect to InventoryService: %v", err)
	}
	defer inventoryConn.Close()

	InventoryClient := inventoryv1.NewInventoryServiceClient(inventoryConn)

	router := chi.NewRouter()

	router.Use(middleware.Logger)
	router.Use(middleware.Recoverer)
	router.Use(middleware.Timeout(10 * time.Second))

	server := &Server{
		storage:         storage,
		inventoryClient: InventoryClient,
	}

	server.registerRoutes()

	httpServer := &http.Server{
		Addr:        net.JoinHostPort("localhost", httpPort),
		Handler:     router,
		ReadTimeout: readHeaderTimeout,
	}

	go func() {
		log.Printf("🚀 HTTP-сервер запущен на порту %s\n", httpPort)
		err = httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("❌ Ошибка запуска сервера: %v\n", err)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("🛑 Завершение работы сервера...")

	// Создаем контекст с таймаутом для остановки сервера
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	err = httpServer.Shutdown(ctx)
	if err != nil {
		log.Printf("❌ Ошибка при остановке сервера: %v\n", err)
	}

	log.Println("✅ Сервер остановлен")
}
