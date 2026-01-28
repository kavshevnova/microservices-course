package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	inventory_v1 "shared/pkg/proto/inventory/v1"
	"sync"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

const grpcPort = 5051

type inventoryService struct {
	inventory_v1.UnimplementedInventoryServiceServer
	mu   sync.RWMutex
	data map[string]*inventory_v1.Part
}

func (i *inventoryService) GetPart(ctx context.Context, req *inventory_v1.GetPartRequest) (*inventory_v1.GetPartResponse, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()

	part, ok := i.data[req.GetUuid()]
	if !ok {
		return nil, status.Error(codes.NotFound, req.GetUuid())
	}
	return &inventory_v1.GetPartResponse{
		Part: part,
	}, nil
}

func (i *inventoryService) ListParts(ctx context.Context, req *inventory_v1.ListPartsRequest) (*inventory_v1.ListPartsResponse, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()

	filter := prepareFilter(req.GetFilter())

	var parts []*inventory_v1.Part

	if filter == nil {
		for _, part := range i.data {
			parts = append(parts, part)
		}
		return &inventory_v1.ListPartsResponse{Parts: parts}, nil
	}

	for _, part := range i.data {
		if matchesFilter(part, filter) {
			parts = append(parts, part)
		}
	}
	return &inventory_v1.ListPartsResponse{Parts: parts}, nil
}

type comlitedFilter struct {
	uuids                 map[string]bool
	names                 map[string]bool
	categories            map[inventory_v1.Category]bool
	manufacturerCountries map[string]bool
	tags                  map[string]bool
}

func prepareFilter(filter *inventory_v1.PartsFilter) *comlitedFilter {
	if filter == nil {
		return nil
	}

	hasAnyFilter := len(filter.Uuids) > 0 ||
		len(filter.Names) > 0 ||
		len(filter.Categories) > 0 ||
		len(filter.ManufacturerCountries) > 0 ||
		len(filter.Tags) > 0

	if !hasAnyFilter {
		return nil // ← Все поля пустые, фильтр не нужен
	}

	cf := &comlitedFilter{}

	if len(filter.Uuids) > 0 {
		cf.uuids = make(map[string]bool)
		for _, uuid := range filter.Uuids {
			cf.uuids[uuid] = true
		}
	}

	if len(filter.Names) > 0 {
		cf.names = make(map[string]bool)
		for _, name := range filter.Names {
			cf.names[name] = true
		}
	}

	if len(filter.Categories) > 0 {
		cf.categories = make(map[inventory_v1.Category]bool)
		for _, category := range filter.Categories {
			cf.categories[category] = true
		}
	}

	if len(filter.ManufacturerCountries) > 0 {
		cf.manufacturerCountries = make(map[string]bool)
		for _, manufacturer := range filter.ManufacturerCountries {
			cf.manufacturerCountries[manufacturer] = true
		}
	}

	if len(filter.Tags) > 0 {
		cf.tags = make(map[string]bool)
		for _, tag := range filter.Tags {
			cf.tags[tag] = true
		}
	}
	return cf
}

func matchesFilter(part *inventory_v1.Part, filter *comlitedFilter) bool {
	if filter.uuids != nil {
		if !filter.uuids[part.Uuid] {
			return false
		}
	}
	if filter.names != nil {
		if !filter.names[part.Name] {
			return false
		}
	}
	if filter.categories != nil {
		if !filter.categories[part.Category] {
			return false
		}
	}
	if filter.manufacturerCountries != nil {
		if !filter.manufacturerCountries[part.Manufacturer.Country] {
			return false
		}
	}
	if filter.tags != nil {
		hasMatchingTag := false
		for _, tag := range part.Tags {
			if filter.tags[tag] {
				hasMatchingTag = true
				break
			}
		}
		if !hasMatchingTag {
			return false
		}
	}
	return true
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

	inventoryGrpcServer := grpc.NewServer()
	service := &inventoryService{
		data: make(map[string]*inventory_v1.Part),
	}

	inventory_v1.RegisterInventoryServiceServer(inventoryGrpcServer, service)

	reflection.Register(inventoryGrpcServer)

	go func() {
		log.Printf("GRPC serever listening on %d\n", grpcPort)
		err = inventoryGrpcServer.Serve(lis)
		if err != nil {
			log.Printf("failed to serve: %v\n", err)
			return
		}
	}()
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Printf("Shutdown Server ...")
	inventoryGrpcServer.GracefulStop()
	log.Printf("Server stopped")
}
