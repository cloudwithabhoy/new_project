package main

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/segmentio/kafka-go"
)

// OrderCreatedEvent is the `order.created` payload (§4). It is consumed by
// inventory (commit reservation), notification (notify), and recommendation
// (rank). Consumers must be idempotent by order_id.
type OrderCreatedEvent struct {
	Event      string      `json:"event"` // always "order.created"
	OrderID    int64       `json:"order_id"`
	UserID     int64       `json:"user_id"`
	Items      []EventItem `json:"items"`
	TotalCents int64       `json:"total_cents"`
	CreatedAt  time.Time   `json:"created_at"`
}

// EventItem is the per-line shape in the event (§4): product_id, quantity,
// price_cents. (No name — the event is leaner than the stored order line.)
type EventItem struct {
	ProductID  int64 `json:"product_id"`
	Quantity   int   `json:"quantity"`
	PriceCents int64 `json:"price_cents"`
}

// Producer wraps a kafka-go Writer for emitting order.created events.
type Producer struct {
	writer *kafka.Writer
	topic  string
}

// NewProducer builds a Kafka writer. The writer is lazy — it dials brokers on
// the first write — so construction won't fail if the broker pod isn't ready
// yet at startup; readiness/retry is handled at write time and the broker may
// come up after us. RequireAll waits for the full ISR so a confirmed order's
// event is durable.
func NewProducer(brokers []string, topic string) *Producer {
	w := &kafka.Writer{
		Addr:                   kafka.TCP(brokers...),
		Topic:                  topic,
		Balancer:               &kafka.Hash{}, // partition by key (order_id) for stable ordering
		RequiredAcks:           kafka.RequireAll,
		AllowAutoTopicCreation: true, // convenient for local dev / Redpanda; harmless in k8s
		WriteTimeout:           10 * time.Second,
	}
	return &Producer{writer: w, topic: topic}
}

// Emit publishes one order.created event keyed by order_id. The key is set so
// partitioning is stable and consumers can dedupe/idempotently process by it.
func (p *Producer) Emit(ctx context.Context, evt OrderCreatedEvent) error {
	value, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	return p.writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(strconv.FormatInt(evt.OrderID, 10)),
		Value: value,
	})
}

// Close flushes and closes the underlying writer during graceful shutdown.
func (p *Producer) Close() error { return p.writer.Close() }
