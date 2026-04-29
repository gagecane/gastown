package mail

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/config"
)

// expandFromConfig is a generic helper for config-based expansion.
// It loads the messaging config and calls the getter to extract the desired value.
// This consolidates the common pattern of: check townRoot, load config, lookup in map.
func expandFromConfig[T any](r *Router, name string, getter func(*config.MessagingConfig) (T, bool), errType error) (T, error) {
	var zero T
	if r.townRoot == "" {
		return zero, fmt.Errorf("%w: %s (no town root)", errType, name)
	}

	configPath := config.MessagingConfigPath(r.townRoot)
	cfg, err := config.LoadMessagingConfig(configPath)
	if err != nil {
		return zero, fmt.Errorf("loading messaging config: %w", err)
	}

	result, ok := getter(cfg)
	if !ok {
		return zero, fmt.Errorf("%w: %s", errType, name)
	}

	return result, nil
}

// expandList returns the recipients for a mailing list.
// Returns ErrUnknownList if the list is not found.
func (r *Router) expandList(listName string) ([]string, error) {
	recipients, err := expandFromConfig(r, listName, func(cfg *config.MessagingConfig) ([]string, bool) {
		r, ok := cfg.Lists[listName]
		return r, ok
	}, ErrUnknownList)
	if err != nil {
		return nil, err
	}

	if len(recipients) == 0 {
		return nil, fmt.Errorf("%w: %s (empty list)", ErrUnknownList, listName)
	}

	return recipients, nil
}

// expandQueue returns the QueueConfig for a queue name.
// Returns ErrUnknownQueue if the queue is not found.
func (r *Router) expandQueue(queueName string) (*config.QueueConfig, error) {
	return expandFromConfig(r, queueName, func(cfg *config.MessagingConfig) (*config.QueueConfig, bool) {
		qc, ok := cfg.Queues[queueName]
		if !ok {
			return nil, false
		}
		return &qc, true
	}, ErrUnknownQueue)
}

// expandAnnounce returns the AnnounceConfig for an announce channel name.
// Returns ErrUnknownAnnounce if the channel is not found.
func (r *Router) expandAnnounce(announceName string) (*config.AnnounceConfig, error) {
	return expandFromConfig(r, announceName, func(cfg *config.MessagingConfig) (*config.AnnounceConfig, bool) {
		ac, ok := cfg.Announces[announceName]
		if !ok {
			return nil, false
		}
		return &ac, true
	}, ErrUnknownAnnounce)
}

// ExpandListAddress expands a list:name address to its recipients.
// Returns ErrUnknownList if the list is not found.
// This is exported for use by commands that want to show fan-out details.
func (r *Router) ExpandListAddress(address string) ([]string, error) {
	if !isListAddress(address) {
		return nil, fmt.Errorf("not a list address: %s", address)
	}
	return r.expandList(parseListName(address))
}
