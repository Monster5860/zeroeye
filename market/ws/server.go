package ws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tent-of-trials/market/matching"
	"github.com/tent-of-trials/market/types"
	"go.uber.org/zap"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type Client struct {
	hub      *Hub
	conn     *websocket.Conn
	send     chan []byte
	subs     map[types.Symbol]struct{}
	remote   string
	mu       sync.Mutex
}

type Hub struct {
	clients    map[*Client]struct{}
	register   chan *Client
	unregister chan *Client
	broadcast  chan []byte
	logger     *zap.Logger
	mu         sync.RWMutex
}

type Server struct {
	hub              *Hub
	engine           *matching.MatchingEngine
	logger           *zap.Logger
	port             int
	srv              *http.Server
	snapshotPath     string
	checksumPath     string
	snapshotInterval time.Duration
	snapshotCancel   context.CancelFunc
}

type persistedOrderBookSnapshot struct {
	Version int                     `json:"version"`
	Books   []persistedBookSnapshot `json:"books"`
}

type persistedBookSnapshot struct {
	Symbol   types.Symbol    `json:"symbol"`
	Snapshot json.RawMessage `json:"snapshot"`
}

func NewHub(logger *zap.Logger) *Hub {
	return &Hub{
		clients:    make(map[*Client]struct{}),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		broadcast:  make(chan []byte, 256),
		logger:     logger,
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = struct{}{}
			h.mu.Unlock()
			h.logger.Info("client connected",
				zap.String("remote", client.remote),
				zap.Int("total", len(h.clients)),
			)

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()
			h.logger.Info("client disconnected",
				zap.String("remote", client.remote),
				zap.Int("total", len(h.clients)),
			)

		case message := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}
			h.mu.RUnlock()
		}
	}
}

func NewServer(hub *Hub, engine *matching.MatchingEngine, logger *zap.Logger, port int) *Server {
	return &Server{
		hub:              hub,
		engine:           engine,
		logger:           logger,
		port:             port,
		snapshotPath:     filepath.Join("data", "orderbook_snapshot.json"),
		checksumPath:     filepath.Join("data", "orderbook_snapshot.sha256"),
		snapshotInterval: snapshotIntervalFromEnv(),
	}
}

func (s *Server) Start() error {
	if err := s.RecoverOrderBooks(); err != nil {
		s.logger.Warn("order book snapshot recovery skipped", zap.Error(err))
	}
	s.startSnapshotLoop()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWebSocket)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/api/v1/trades", s.handleGetTrades)
	mux.HandleFunc("/api/v1/depth", s.handleGetDepth)
	mux.HandleFunc("/admin/orderbook/snapshot", s.handleAdminSnapshot)

	s.srv = &http.Server{
		Addr:         fmt.Sprintf(":%d", s.port),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return s.srv.ListenAndServe()
}

func (s *Server) Stop() {
	if s.snapshotCancel != nil {
		s.snapshotCancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.srv.Shutdown(ctx)
}

func (s *Server) SnapshotOrderBooks() ([]byte, error) {
	books := s.engine.Books()
	symbols := make([]string, 0, len(books))
	for symbol := range books {
		symbols = append(symbols, string(symbol))
	}
	sort.Strings(symbols)

	snapshot := persistedOrderBookSnapshot{
		Version: 1,
		Books:   make([]persistedBookSnapshot, 0, len(symbols)),
	}
	for _, symbolName := range symbols {
		symbol := types.Symbol(symbolName)
		bookSnapshot, err := books[symbol].Snapshot()
		if err != nil {
			return nil, err
		}
		snapshot.Books = append(snapshot.Books, persistedBookSnapshot{
			Symbol:   symbol,
			Snapshot: json.RawMessage(bookSnapshot),
		})
	}
	return json.MarshalIndent(snapshot, "", "  ")
}

func (s *Server) WriteOrderBookSnapshot() error {
	body, err := s.SnapshotOrderBooks()
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	checksum := hex.EncodeToString(sum[:])

	if err := os.MkdirAll(filepath.Dir(s.snapshotPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(s.snapshotPath, body, 0o644); err != nil {
		return err
	}
	return os.WriteFile(s.checksumPath, []byte(checksum+"\n"), 0o644)
}

func (s *Server) RecoverOrderBooks() error {
	body, err := os.ReadFile(s.snapshotPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	checksumBytes, err := os.ReadFile(s.checksumPath)
	if err != nil {
		return err
	}
	expected := string(checksumBytes)
	for len(expected) > 0 && (expected[len(expected)-1] == '\n' || expected[len(expected)-1] == '\r') {
		expected = expected[:len(expected)-1]
	}
	actualSum := sha256.Sum256(body)
	actual := hex.EncodeToString(actualSum[:])
	if expected != actual {
		return fmt.Errorf("order book snapshot checksum mismatch")
	}

	var snapshot persistedOrderBookSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return err
	}

	books := s.engine.Books()
	for _, bookSnapshot := range snapshot.Books {
		book, ok := books[bookSnapshot.Symbol]
		if !ok {
			return fmt.Errorf("snapshot contains unknown symbol %s", bookSnapshot.Symbol)
		}
		if err := book.Recover(bookSnapshot.Snapshot); err != nil {
			return err
		}
	}
	s.logger.Info("order book snapshot recovered",
		zap.String("path", s.snapshotPath),
		zap.Int("books", len(snapshot.Books)),
	)
	return nil
}

func (s *Server) startSnapshotLoop() {
	if s.snapshotInterval <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.snapshotCancel = cancel
	go func() {
		ticker := time.NewTicker(s.snapshotInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := s.WriteOrderBookSnapshot(); err != nil {
					s.logger.Warn("order book snapshot write failed", zap.Error(err))
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("websocket upgrade failed", zap.Error(err))
		return
	}

	client := &Client{
		hub:    s.hub,
		conn:   conn,
		send:   make(chan []byte, 256),
		subs:   make(map[types.Symbol]struct{}),
		remote: r.RemoteAddr,
	}

	s.hub.register <- client

	go client.writePump()
	go client.readPump()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "ok",
		"service": "tent-market",
		"time":    time.Now().Unix(),
	})
}

func (s *Server) handleGetTrades(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	trades := s.engine.GetRecentTrades(100)
	json.NewEncoder(w).Encode(trades)
}

func (s *Server) handleGetDepth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"message": "depth endpoint"})
}

func (s *Server) handleAdminSnapshot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	if err := s.WriteOrderBookSnapshot(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{
		"status":        "ok",
		"snapshot_path": s.snapshotPath,
		"checksum_path": s.checksumPath,
	})
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(65536)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			break
		}

		var event map[string]interface{}
		if err := json.Unmarshal(message, &event); err != nil {
			continue
		}

		c.mu.Lock()

		c.mu.Unlock()
	}
}

func snapshotIntervalFromEnv() time.Duration {
	value := os.Getenv("OB_SNAPSHOT_INTERVAL_SECS")
	if value == "" {
		return 60 * time.Second
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 0 {
		return 60 * time.Second
	}
	return time.Duration(seconds) * time.Second
}

func (c *Client) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
