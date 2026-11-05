package main

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
)

// Product is a catalog entry as the wire shows it.
type Product struct {
	ID    int64
	Name  string
	SKU   string
	Price float64
}

var (
	// ErrNotFound means no product has the id.
	ErrNotFound = errors.New("catalog: no such product")
	// ErrDuplicateSKU means another product already has the SKU.
	ErrDuplicateSKU = errors.New("catalog: duplicate sku")
)

// Store is where products live; handlers reach it through a Products service.
type Store interface {
	List(ctx context.Context, name string, offset, limit int) (items []Product, total int, err error)
	Get(ctx context.Context, id int64) (Product, error)
	Create(ctx context.Context, p Product) (Product, error)
	Update(ctx context.Context, p Product) (Product, error)
	Delete(ctx context.Context, id int64) error
}

// MemStore is a Store in memory.
type MemStore struct {
	mu   sync.RWMutex
	next int64
	byID map[int64]Product
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{next: 1, byID: map[int64]Product{}}
}

func (m *MemStore) skuTaken(sku string, except int64) bool {
	for _, p := range m.byID {
		if p.SKU == sku && p.ID != except {
			return true
		}
	}
	return false
}

func (m *MemStore) List(_ context.Context, name string, offset, limit int) ([]Product, int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var all []Product
	for _, p := range m.byID {
		if name == "" || strings.Contains(strings.ToLower(p.Name), strings.ToLower(name)) {
			all = append(all, p)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	total := len(all)
	if offset > total {
		offset = total
	}
	end := min(offset+limit, total)
	return all[offset:end], total, nil
}

func (m *MemStore) Get(_ context.Context, id int64) (Product, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.byID[id]
	if !ok {
		return Product{}, ErrNotFound
	}
	return p, nil
}

func (m *MemStore) Create(_ context.Context, p Product) (Product, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.skuTaken(p.SKU, 0) {
		return Product{}, ErrDuplicateSKU
	}
	p.ID = m.next
	m.next++
	m.byID[p.ID] = p
	return p, nil
}

func (m *MemStore) Update(_ context.Context, p Product) (Product, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[p.ID]; !ok {
		return Product{}, ErrNotFound
	}
	if m.skuTaken(p.SKU, p.ID) {
		return Product{}, ErrDuplicateSKU
	}
	m.byID[p.ID] = p
	return p, nil
}

func (m *MemStore) Delete(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[id]; !ok {
		return ErrNotFound
	}
	delete(m.byID, id)
	return nil
}
