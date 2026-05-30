package core

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

type ticketInfo struct {
	nodeID    string
	expiresAt time.Time
}

var ticketStore sync.Map

// 签发有效期 10 秒的单次 Ticket
func GenerateWSTicket(nodeID string) string {
	b := make([]byte, 16)
	rand.Read(b)
	ticket := hex.EncodeToString(b)

	ticketStore.Store(ticket, &ticketInfo{
		nodeID:    nodeID,
		expiresAt: time.Now().Add(10 * time.Second),
	})
	return ticket
}

// 校验并销毁 Ticket（保证一次性）
func ValidateAndConsumeTicket(ticket, requestedNodeID string) bool {
	val, ok := ticketStore.LoadAndDelete(ticket)
	if !ok {
		return false
	}
	
	info := val.(*ticketInfo)
	if time.Now().After(info.expiresAt) || info.nodeID != requestedNodeID {
		return false
	}
	return true
}