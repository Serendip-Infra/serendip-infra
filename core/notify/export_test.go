package notify

import (
	"net/netip"
	"time"
)

// Exempt lets the address check pass p, for this package's tests only:
// their servers listen on loopback.
func (c *Checker) Exempt(p netip.Prefix) {
	c.mu.Lock()
	c.exempt = append(c.exempt, p)
	c.mu.Unlock()
}

// SetTelegramBase points the client at a fake Bot API.
func (c *Client) SetTelegramBase(base string) { c.telegramBase = base }

// SetNow fixes the client's clock.
func (c *Client) SetNow(now func() time.Time) { c.now = now }

// Control runs the dialer's Control hook on address.
func (c *Client) Control(address string, allowPrivate bool) error {
	return c.dial.control(allowPrivate)("tcp", address, nil)
}

var TelegramBody = telegramBody
var EmailMessage = emailMessage
var WebhookBodyOf = webhookBody
