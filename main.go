package main

import (
	"fmt"
	"log"
	"net/http"
	"strconv"

	"sync/atomic"

	"sync"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
)

type Message struct {
	RoomID       string                    `json:"roomId"`
	UserID       string                    `json:"userId"`
	TargetUserID string                    `json:"targetUserId"`
	Signal       string                    `json:"signal"`    // "offer", "answer", "candidate", "chat", "audio_offer", "audio_answer"
	MediaType    string                    `json:"mediaType"` // "audio", "video"
	SDP          webrtc.SessionDescription `json:"sdp"`       // SDP数据
	Candidate    webrtc.ICECandidateInit   `json:"candidate"` //Candidate数据
	Data         string                    `json:"data"`
	UserIDs      []string                  `json:"userIds"`
}

type RoomConnections struct {
	RoomConn *websocket.Conn
	UserID   string
}

const (
	socketUpgraderReadBufferSize  = 1024
	socketUpgraderWriteBufferSize = 1024
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  socketUpgraderReadBufferSize,
	WriteBufferSize: socketUpgraderWriteBufferSize,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

var rooms = make(map[string][]RoomConnections)

var broadcast = make(chan Message)

var userIDCounter uint64

var mutex sync.Mutex

// var globalPCMap = &sync.Map{}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	roomId := r.URL.Query().Get("roomId")
	if roomId == "" {
		http.Error(w, "Missing roomId query parameter", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Error upgrading to WebSocket:", err)
		return
	}
	defer func() {
		removeClientFromRoom(roomId, conn, "")
		conn.Close()
	}()

	currentUserID := atomic.AddUint64(&userIDCounter, 1)
	userID := "user" + strconv.FormatUint(currentUserID-1, 10)

	rooms[roomId] = append(rooms[roomId], RoomConnections{RoomConn: conn, UserID: userID})

	joinMsg := Message{RoomID: roomId, UserID: userID, Signal: "join", Data: userID + " has joined the room"}
	broadcast <- joinMsg

	userids, _ := getUsersInRoom(roomId)
	log.Println("userids", len(userids))

	err = sendMessage(conn, Message{RoomID: roomId, UserID: userID, Signal: "userId", Data: userID, UserIDs: userids})
	if err != nil {
		log.Println("Error writing userID to new client:", err)
		return
	}

	for {
		var msg Message
		err := conn.ReadJSON(&msg)
		if err != nil {
			log.Println("Error reading message:", err)

			removeClientFromRoom(roomId, conn, userID)
			break
		}

		handleWebRTCMessage(msg)
	}
}

func handleWebRTCMessage(msg Message) {

	switch msg.Signal {
	case "offer":
		log.Println("offer")
		if msg.TargetUserID != "" {
			targetMsg := msg // 复制消息以避免并发修改
			go sendToTargetUser(targetMsg, msg.RoomID, msg.TargetUserID)
		} else {
			log.Println("No targetUserID specified in the offer message.")
		}
		break
	case "answer":
		log.Println("answer")
		if msg.TargetUserID != "" {
			targetMsg := msg
			go sendToTargetUser(targetMsg, msg.RoomID, msg.TargetUserID)
		} else {
			log.Println("No targetUserID specified in the answer message.")
		}
		break

	case "candidate":
		log.Println("candidate")
		if msg.TargetUserID != "" {
			targetMsg := msg
			go sendToTargetUser(targetMsg, msg.RoomID, msg.TargetUserID)
		} else {
			log.Println("No targetUserID specified in the candidate message.")
		}
		break

	default:
		broadcast <- msg
		log.Println("get msg:", msg.Data)
	}
}

func getUsersInRoom(roomID string) ([]string, error) {
	roomClients, ok := rooms[roomID]
	if !ok {
		return nil, fmt.Errorf("room not found with ID: %s", roomID)
	}

	var userIds []string
	for _, client := range roomClients {
		userIds = append(userIds, client.UserID)
	}

	return userIds, nil
}

func sendToTargetUser(msg Message, roomId, targetUserID string) {
	roomClients, ok := rooms[roomId]
	if !ok {
		log.Printf("Room not found with ID: %s", roomId)
		return
	}

	for _, client := range roomClients {
		if client.UserID == targetUserID {
			log.Printf("send msg to : %s", targetUserID)
			err := sendMessage(client.RoomConn, msg)
			if err != nil {
				log.Printf("Error sending to target user %s: %v", targetUserID, err)
				// 可能需要处理错误情况，比如从房间移除该客户端等
			}
			return
		}
	}
	log.Printf("Target user %s not found in room: %s", targetUserID, roomId)
}

func removeClientFromRoom(roomId string, conn *websocket.Conn, userID string) {
	roomClients, ok := rooms[roomId]
	if !ok {
		return
	}

	// 找到并移除RoomConnections中的连接
	for i, c := range roomClients {
		if c.RoomConn == conn {
			// 从房间中移除这个连接
			roomClients = append(roomClients[:i], roomClients[i+1:]...)
			rooms[roomId] = roomClients

			// 广播用户离开的消息
			if userID != "" {
				leaveMsg := Message{RoomID: roomId, Signal: "leave", UserID: userID, Data: userID + " has left the room"}
				broadcast <- leaveMsg
			}

			break
		}
	}
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	http.FileServer(http.Dir("./static")).ServeHTTP(w, r)
}

func startServer() {
	http.HandleFunc("/ws", handleWebSocket)
	http.HandleFunc("/", handleRoot)

	log.Println("Starting server on localhost:8080...")
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
func sendMessage(c *websocket.Conn, msg any) error {
	// var mutex sync.Mutex
	log.Printf("sendMessage: ", msg)
	mutex.Lock()
	defer mutex.Unlock()
	return c.WriteJSON(msg)
}

func main() {
	go func() {
		for {
			msg := <-broadcast
			roomClients, ok := rooms[msg.RoomID]
			if !ok {
				log.Println("No clients in room:", msg.RoomID)
				continue
			}
			for _, client := range roomClients {
				err := sendMessage(client.RoomConn, msg)
				if err != nil {
					log.Println("Error writing to client:", err)
					removeClientFromRoom(msg.RoomID, client.RoomConn, msg.UserID)
				}
			}
		}
	}()

	startServer()
}
