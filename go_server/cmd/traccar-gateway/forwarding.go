package main

import (
	"log"
	"net/http"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/forward"
)

// forwardConfig reads the forwarder keys. The Java names select the JSON and URL
// destinations; the mqtt, redis and wialon sections are Go additions, and
// forward.kind decides which of the two feeds the non-HTTP transports carry.
type forwardConfig struct {
	queue    int
	attempts int
	backoff  time.Duration
	timeout  time.Duration
	kind     string
}

func readForwardConfig(cfg *config.Config) forwardConfig {
	settings := forwardConfig{
		queue:    forward.DefaultQueueSize,
		attempts: forward.DefaultMaxAttempts,
		backoff:  forward.DefaultMaxBackoff,
		timeout:  forward.DefaultTimeout,
		kind:     "all",
	}
	if cfg == nil {
		return settings
	}

	if value, err := cfg.Int(config.ForwardQueue); err == nil && value > 0 {
		settings.queue = value
	} else if err != nil {
		log.Printf("forward: %v", err)
	}
	if value, err := cfg.Int(config.ForwardAttempts); err == nil && value > 0 {
		settings.attempts = value
	} else if err != nil {
		log.Printf("forward: %v", err)
	}
	if value, err := cfg.Duration(config.ForwardBackoff); err == nil && value > 0 {
		settings.backoff = value
	} else if err != nil {
		log.Printf("forward: %v", err)
	}
	if value, err := cfg.Duration(config.ForwardTimeout); err == nil && value > 0 {
		settings.timeout = value
	} else if err != nil {
		log.Printf("forward: %v", err)
	}
	switch kind := strings.ToLower(strings.TrimSpace(cfg.String(config.ForwardKind))); kind {
	case "position", "event", "all":
		settings.kind = kind
	case "":
	default:
		log.Printf("forward: unknown forward.kind %q, forwarding both feeds", kind)
	}
	return settings
}

// buildForwarders creates one transport per configured destination. A
// destination that fails to build is logged and skipped, so one bad key does
// not disable the rest of the feeds.
func buildForwarders(cfg *config.Config, settings forwardConfig) []forward.Transport {
	if cfg == nil {
		return nil
	}
	client := &http.Client{Timeout: settings.timeout}

	var transports []forward.Transport
	add := func(transport forward.Transport, err error) {
		if err != nil {
			log.Printf("forward: %v", err)
			return
		}
		transports = append(transports, transport)
	}

	// The position and event URL forwarders are the Java forward.url and
	// event.forward.url keys. forward.type picks the destination: json or url
	// posts over HTTP, amqp publishes to an exchange and kafka produces to a
	// topic. Each one only receives its own feed.
	if url := cfg.String(config.ForwardURL); url != "" {
		transport, err := newForwarder(url, cfg.StringOr(config.ForwardType, "json"),
			cfg.String(config.ForwardHeader),
			cfg.StringOr(config.ForwardExchange, "traccar"),
			cfg.StringOr(config.ForwardTopic, "positions"), client)
		if err == nil {
			transport = forward.Only(transport, false)
		}
		add(transport, err)
	}
	if url := cfg.String(config.EventForwardURL); url != "" {
		transport, err := newForwarder(url, cfg.StringOr(config.EventForwardType, "json"),
			cfg.String(config.EventForwardHeader),
			cfg.StringOr(config.EventForwardExchange, "traccar"),
			cfg.StringOr(config.EventForwardTopic, "events"), client)
		if err == nil {
			transport = forward.Only(transport, true)
		}
		add(transport, err)
	}

	if broker := cfg.String(config.ForwardMQTTBroker); broker != "" {
		transport, err := forward.NewMQTT(forward.MQTTConfig{
			Broker:   broker,
			Topic:    cfg.String(config.ForwardMQTTTopic),
			ClientID: cfg.String(config.ForwardMQTTClientID),
			Username: cfg.String(config.ForwardMQTTUser),
			Password: cfg.String(config.ForwardMQTTPassword),
		})
		add(onlyForKind(transport, settings.kind), err)
	}

	// The Redis list carries positions under the device uniqueId, so it takes
	// the position feed only.
	if url := cfg.String(config.ForwardRedisURL); url != "" {
		transport, err := forward.NewRedis(forward.RedisConfig{
			URL:       url,
			KeyPrefix: cfg.String(config.ForwardRedisKeyPrefix),
		})
		var limited forward.Transport
		if err == nil {
			limited = forward.Only(transport, false)
		}
		add(limited, err)
	}

	// A plain socket destination receives the same JSON as the HTTP one. It is
	// the Java NetworkForwarder: useful when the consumer is a small service
	// that only listens on a port.
	if address := cfg.String(config.ForwardNetworkAddress); address != "" {
		transport, err := forward.NewNetwork(forward.NetworkConfig{
			Address:  address,
			Protocol: cfg.StringOr(config.ForwardNetworkProtocol, "tcp"),
		})
		add(onlyForKind(transport, settings.kind), err)
	}

	// Wialon IPS carries positions only, so the address alone enables it.
	if address := cfg.String(config.ForwardWialonAddress); address != "" {
		transport, err := forward.NewWialon(forward.WialonConfig{
			Address: address,
			Version: cfg.String(config.ForwardWialonVersion),
		})
		var limited forward.Transport
		if err == nil {
			limited = forward.Only(transport, false)
		}
		add(limited, err)
	}

	return transports
}

// newForwarder picks the destination by the configured type. json and url post
// over HTTP, amqp and kafka hand the same document to a broker.
func newForwarder(target, kind, header, exchange, topic string, client *http.Client) (forward.Transport, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "json":
		return forward.NewJSON(forward.HTTPConfig{URL: target, Header: header, Client: client})
	case "url":
		return forward.NewURL(forward.HTTPConfig{URL: target, Header: header, Client: client})
	case "amqp":
		return forward.NewAMQP(forward.AMQPConfig{URL: target, Exchange: exchange, Topic: topic})
	case "kafka":
		return forward.NewKafka(forward.KafkaConfig{Brokers: target, Topic: topic})
	default:
		log.Printf("forward: unknown type %q, using json", kind)
		return forward.NewJSON(forward.HTTPConfig{URL: target, Header: header, Client: client})
	}
}

// transportNames labels the enabled destinations in the startup log.
func transportNames(transports []forward.Transport) string {
	names := make([]string, 0, len(transports))
	for _, transport := range transports {
		if transport != nil {
			names = append(names, transport.Name())
		}
	}
	return strings.Join(names, ", ")
}

// onlyForKind restricts a transport to one feed when forward.kind says so.
func onlyForKind(transport forward.Transport, kind string) forward.Transport {
	switch kind {
	case "position":
		return forward.Only(transport, false)
	case "event":
		return forward.Only(transport, true)
	default:
		return transport
	}
}
