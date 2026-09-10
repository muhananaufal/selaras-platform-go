package handler

import (
	"github.com/gin-gonic/gin"
)

// The shape of the response envelope is part of the contract, not a writing
// detail. The keys are collected here so a typo - "mesage" instead of
// "message" - fails at compile time instead of silently breaking a client
// looking for a key that never arrives.
const (
	keyData    = "data"
	keyMessage = "message"
	keySuccess = "success"
)

// writeData sends one resource inside the `data` envelope.
func writeData(c *gin.Context, status int, data any) {
	c.JSON(status, gin.H{keyData: data})
}

// writeDataWithMessage is used by endpoints that already send a message
// along with their data today; that shape is kept so the frontend need not
// change.
func writeDataWithMessage(c *gin.Context, status int, message string, data any) {
	c.JSON(status, gin.H{keyMessage: message, keyData: data})
}

// writeMessage sends an answer that carries no resource at all.
func writeMessage(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{keySuccess: true, keyMessage: message})
}
